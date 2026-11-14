package aiharness

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const DefaultInstruction = "Write the next paragraph at the specified UUID placeholder. Match the document's language, voice, and notation. Return only the paragraph's Markdown or LaTeX source, without fences, explanations, notes, or placeholder markers. Do not repeat surrounding text."

type Request struct {
	UUID            string
	Document        string
	Instruction     string
	Context         string
	CurrentDocument string
	Model           *Model
}

type Record struct {
	Request
	Version   int
	Prompt    string
	Status    string
	Paragraph string
	Error     string
}

func Prompt(r Request) string {
	return "Instruction:\n" + r.Instruction + "\n\nTarget placeholder UUID: " + r.UUID + "\n\nContext:\n" + r.Context + "\n\nCurrent document:\n" + r.CurrentDocument
}

type Harness struct {
	db        *sql.DB
	generator Generator
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	tasks     map[string]context.CancelFunc
	closed    bool
	wg        sync.WaitGroup
	serial    chan struct{}
	updates   chan Record
}

func Open(path string, generator Generator) (*Harness, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=3000; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS govim_ai_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT OR IGNORE INTO govim_ai_meta VALUES('schema','1');
CREATE TABLE IF NOT EXISTS govim_ai_versions(
uuid TEXT NOT NULL, version INTEGER NOT NULL, document TEXT NOT NULL,
instruction TEXT NOT NULL, context TEXT NOT NULL, document_text TEXT NOT NULL,
model TEXT NOT NULL, prompt TEXT NOT NULL, status TEXT NOT NULL,
paragraph TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
created INTEGER NOT NULL, updated INTEGER NOT NULL, PRIMARY KEY(uuid,version));
CREATE TABLE IF NOT EXISTS govim_ai_events(
id INTEGER PRIMARY KEY, uuid TEXT NOT NULL, version INTEGER NOT NULL,
kind TEXT NOT NULL, text TEXT NOT NULL, created INTEGER NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	var schema string
	if err := db.QueryRow("SELECT value FROM govim_ai_meta WHERE key='schema'").Scan(&schema); err != nil || schema != "1" {
		db.Close()
		return nil, fmt.Errorf("unsupported AI project schema")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Harness{db: db, generator: generator, ctx: ctx, cancel: cancel, tasks: make(map[string]context.CancelFunc), serial: make(chan struct{}, 1), updates: make(chan Record, 32)}, nil
}

func (h *Harness) Updates() <-chan Record   { return h.updates }
func taskKey(id string, version int) string { return fmt.Sprintf("%s:%d", id, version) }

func (h *Harness) Active(r Record) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.tasks[taskKey(r.UUID, r.Version)]
	return ok
}

func (h *Harness) Latest(id string) (Record, error) {
	var r Record
	var model string
	err := h.db.QueryRow(`SELECT uuid,version,document,instruction,context,document_text,model,prompt,status,paragraph,error
FROM govim_ai_versions WHERE uuid=? ORDER BY version DESC LIMIT 1`, id).Scan(
		&r.UUID, &r.Version, &r.Document, &r.Instruction, &r.Context, &r.CurrentDocument, &model, &r.Prompt, &r.Status, &r.Paragraph, &r.Error)
	if err == nil {
		err = json.Unmarshal([]byte(model), &r.Model)
	}
	return r, err
}

// States is the small dictionary view used by status displays. It deliberately
// avoids loading each request's potentially multi-megabyte prompt snapshots.
func (h *Harness) States(document string) (map[string]string, error) {
	rows, err := h.db.Query(`SELECT v.uuid,v.status FROM govim_ai_versions v
JOIN (SELECT uuid,MAX(version) version FROM govim_ai_versions GROUP BY uuid) latest
ON latest.uuid=v.uuid AND latest.version=v.version WHERE v.document=?`, document)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make(map[string]string)
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, err
		}
		states[id] = status
	}
	return states, rows.Err()
}

// Submit freezes every prompt input before dispatch. No transport retry can
// accidentally generate a second paragraph; a retry is a new dictionary version.
func (h *Harness) Submit(request Request) (Record, error) {
	return h.SubmitWith(request, h.generator)
}

// SubmitWith freezes a backend per request, allowing documents sharing a project
// database to select different servers without redirecting already queued work.
func (h *Harness) SubmitWith(request Request, generator Generator) (Record, error) {
	if request.Instruction == "" {
		request.Instruction = DefaultInstruction
	}
	if !ValidUUID(request.UUID) || request.Document == "" || !utf8.ValidString(request.CurrentDocument+request.Context+request.Instruction) {
		return Record{}, fmt.Errorf("invalid AI request")
	}
	if len(request.CurrentDocument)+len(request.Context)+len(request.Instruction) > MaxInput {
		return Record{}, fmt.Errorf("AI instruction, context and document exceed %d bytes", MaxInput)
	}
	block, err := FindBlock(request.CurrentDocument, request.UUID)
	if err != nil {
		return Record{}, err
	}
	if string([]rune(request.CurrentDocument)[block.Content:block.Close]) != Slot {
		return Record{}, fmt.Errorf("AI target must contain an empty %s slot", Slot)
	}
	if request.Model != nil {
		model := *request.Model
		request.Model = &model
	}
	model, err := json.Marshal(request.Model)
	if err != nil {
		return Record{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return Record{}, fmt.Errorf("AI harness is closed")
	}
	if generator == nil {
		return Record{}, fmt.Errorf("AI generation is not configured")
	}
	if len(h.tasks) >= 16 {
		return Record{}, fmt.Errorf("AI request queue is full")
	}
	// This single SQL statement allocates the next version across editors too.
	r := Record{Request: request, Prompt: Prompt(request), Status: "queued"}
	now := time.Now().UnixNano()
	err = h.db.QueryRow(`INSERT INTO govim_ai_versions
(uuid,version,document,instruction,context,document_text,model,prompt,status,created,updated)
SELECT ?,COALESCE(MAX(version),0)+1,?,?,?,?,?,?,?, ?,? FROM govim_ai_versions WHERE uuid=? RETURNING version`,
		r.UUID, r.Document, r.Instruction, r.Context, r.CurrentDocument, string(model), r.Prompt, r.Status, now, now, r.UUID).Scan(&r.Version)
	if err != nil {
		return Record{}, err
	}
	ctx, cancel := context.WithCancel(h.ctx)
	h.tasks[taskKey(r.UUID, r.Version)] = cancel
	h.wg.Add(1)
	go h.run(ctx, r, generator)
	if r.Model != nil {
		model := *r.Model
		r.Model = &model
	}
	return r, nil
}

func (h *Harness) run(ctx context.Context, r Record, generator Generator) {
	defer h.wg.Done()
	defer func() {
		h.mu.Lock()
		key := taskKey(r.UUID, r.Version)
		h.tasks[key]()
		delete(h.tasks, key)
		h.mu.Unlock()
	}()
	select {
	case h.serial <- struct{}{}:
		defer func() { <-h.serial }()
	case <-ctx.Done():
		h.complete(r, "unknown", "", "request interrupted before completion")
		return
	}
	result, err := h.db.Exec("UPDATE govim_ai_versions SET status='running',updated=? WHERE uuid=? AND version=? AND status='queued'", time.Now().UnixNano(), r.UUID, r.Version)
	if err != nil {
		h.complete(r, "failed", "", err.Error())
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return
	}
	if ctx.Err() != nil {
		h.complete(r, "unknown", "", "request interrupted before completion")
		return
	}
	text, err := generator.Generate(ctx, r.Prompt, r.Model)
	if ctx.Err() != nil {
		h.complete(r, "unknown", "", "request interrupted; discard and retry explicitly")
		return
	}
	if err != nil {
		h.complete(r, "failed", "", err.Error())
		return
	}
	text = strings.TrimSpace(text)
	block, blockErr := FindBlock(BlockText(r.UUID, text), r.UUID)
	if text == "" || len(text) > MaxParagraph || !utf8.ValidString(text) || strings.Contains(text, Slot) || strings.Contains(text, "{{{%") || blockErr != nil || block.End != utf8.RuneCountInString(BlockText(r.UUID, text)) {
		h.complete(r, "failed", "", "invalid paragraph: empty, oversized, or contains unmatched/reserved block markers")
		return
	}
	h.complete(r, "ready", text, "")
}

func (h *Harness) complete(r Record, status, text, failure string) {
	result, err := h.db.Exec(`UPDATE govim_ai_versions SET status=?,paragraph=?,error=?,updated=?
WHERE uuid=? AND version=? AND status IN ('queued','running')`, status, text, failure, time.Now().UnixNano(), r.UUID, r.Version)
	if err != nil {
		r.Status, r.Error = "failed", "persist AI result: "+err.Error()
	} else {
		if count, _ := result.RowsAffected(); count != 1 {
			return
		} // discarded/superseded work
		r.Status, r.Paragraph, r.Error = status, text, failure
	}
	select {
	case h.updates <- r:
	case <-h.ctx.Done():
	}
}

// RecordAction retains the exact accepted/discarded text as an append-only event.
// Paragraph versions are retained even after UnIns and subsequent regeneration.
func (h *Harness) RecordAction(r Record, kind, text string) error {
	if kind != "inserted" && kind != "discarded" {
		return fmt.Errorf("invalid insertion action")
	}
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE govim_ai_versions SET status=?,updated=? WHERE uuid=? AND version=?", kind, time.Now().UnixNano(), r.UUID, r.Version)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("AI dictionary version is missing")
	}
	if _, err := tx.Exec("INSERT INTO govim_ai_events(uuid,version,kind,text,created) VALUES(?,?,?,?,?)", r.UUID, r.Version, kind, text, time.Now().UnixNano()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if kind == "discarded" {
		h.mu.Lock()
		if cancel := h.tasks[taskKey(r.UUID, r.Version)]; cancel != nil {
			cancel()
		}
		h.mu.Unlock()
	}
	return nil
}

func (h *Harness) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.cancel()
	h.mu.Unlock()
	h.wg.Wait()
	return h.db.Close()
}
