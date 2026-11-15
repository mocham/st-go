package aiharness

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const DefaultInstruction = "Write one insertion paragraph for each requested UUID placeholder. Plan the paragraphs together in document order. Match the document's language, voice, and notation. Do not repeat surrounding text. Follow the JSON response format below."

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

type generationTask struct {
	cancel    context.CancelFunc
	remaining int
}

type Harness struct {
	db        *sql.DB
	generator Generator
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	tasks     map[string]*generationTask
	jobs      int
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
	return &Harness{db: db, generator: generator, ctx: ctx, cancel: cancel, tasks: make(map[string]*generationTask), serial: make(chan struct{}, 1), updates: make(chan Record, 32)}, nil
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

// Submit is the single-slot form of SubmitBatch, using the same JSON contract.
func (h *Harness) Submit(request Request) (Record, error) {
	return h.SubmitWith(request, h.generator)
}

func (h *Harness) SubmitWith(request Request, generator Generator) (Record, error) {
	records, err := h.SubmitBatchWith(singleBatch(request), generator)
	if err != nil {
		return Record{}, err
	}
	return records[0], nil
}

func (h *Harness) SubmitBatch(request BatchRequest) ([]Record, error) {
	return h.SubmitBatchWith(request, h.generator)
}

// SubmitBatchWith reserves all UUID versions atomically, then dispatches exactly
// one model request. It freezes both the backend and every prompt input.
func (h *Harness) SubmitBatchWith(request BatchRequest, generator Generator) ([]Record, error) {
	if request.Instruction == "" {
		request.Instruction = DefaultInstruction
	}
	request.UUIDs = append([]string(nil), request.UUIDs...)
	if err := validateBatch(request); err != nil {
		return nil, err
	}
	if request.Model != nil {
		model := *request.Model
		request.Model = &model
	}
	model, err := json.Marshal(request.Model)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, fmt.Errorf("AI harness is closed")
	}
	if generator == nil {
		return nil, fmt.Errorf("AI generation is not configured")
	}
	if h.jobs >= 16 {
		return nil, fmt.Errorf("AI batch queue is full")
	}
	tx, err := h.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	prompt := BatchPrompt(request)
	records := make([]Record, 0, len(request.UUIDs))
	now := time.Now().UnixNano()
	for _, id := range request.UUIDs {
		r := Record{Request: Request{id, request.Document, request.Instruction, request.Context, request.CurrentDocument, request.Model}, Prompt: prompt, Status: "queued"}
		// Version allocation and all batch entries commit together, across editors.
		err = tx.QueryRow(`INSERT INTO govim_ai_versions
(uuid,version,document,instruction,context,document_text,model,prompt,status,created,updated)
SELECT ?,COALESCE(MAX(version),0)+1,?,?,?,?,?,?,?, ?,? FROM govim_ai_versions WHERE uuid=? RETURNING version`,
			r.UUID, r.Document, r.Instruction, r.Context, r.CurrentDocument, string(model), r.Prompt, r.Status, now, now, r.UUID).Scan(&r.Version)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(h.ctx)
	task := &generationTask{cancel: cancel, remaining: len(records)}
	for _, r := range records {
		h.tasks[taskKey(r.UUID, r.Version)] = task
	}
	h.jobs++
	h.wg.Add(1)
	go h.runBatch(ctx, records, task, generator)
	out := append([]Record(nil), records...)
	for i := range out {
		if out[i].Model != nil {
			model := *out[i].Model
			out[i].Model = &model
		}
	}
	return out, nil
}

func (h *Harness) runBatch(ctx context.Context, records []Record, task *generationTask, generator Generator) {
	defer h.wg.Done()
	defer func() {
		h.mu.Lock()
		task.cancel()
		for _, r := range records {
			delete(h.tasks, taskKey(r.UUID, r.Version))
		}
		h.jobs--
		h.mu.Unlock()
	}()
	select {
	case h.serial <- struct{}{}:
		defer func() { <-h.serial }()
	case <-ctx.Done():
		h.completeBatch(records, "unknown", nil, "request interrupted before completion")
		return
	}
	started, err := h.storeBatch(records, "running", nil, "")
	if err != nil {
		h.completeBatch(records, "failed", nil, err.Error())
		return
	}
	if len(started) == 0 {
		return
	}
	if ctx.Err() != nil {
		h.completeBatch(records, "unknown", nil, "request interrupted before completion")
		return
	}
	text, err := generator.Generate(ctx, records[0].Prompt, records[0].Model)
	if ctx.Err() != nil {
		h.completeBatch(records, "unknown", nil, "request interrupted; discard and retry explicitly")
		return
	}
	if err != nil {
		h.completeBatch(records, "failed", nil, err.Error())
		return
	}
	ids := make([]string, len(records))
	for i, r := range records {
		ids[i] = r.UUID
	}
	paragraphs, err := ParseInsertions(text, ids)
	if err != nil {
		h.completeBatch(records, "failed", nil, err.Error())
		return
	}
	h.completeBatch(records, "ready", paragraphs, "")
}

// All surviving entries reach the new state before any completion is announced.
// Discarded entries are excluded even when their shared request returns later.
func (h *Harness) storeBatch(records []Record, status string, paragraphs map[string]string, failure string) ([]Record, error) {
	tx, err := h.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var updated []Record
	for _, r := range records {
		text := paragraphs[r.UUID]
		result, err := tx.Exec(`UPDATE govim_ai_versions SET status=?,paragraph=?,error=?,updated=?
WHERE uuid=? AND version=? AND status IN ('queued','running')`, status, text, failure, time.Now().UnixNano(), r.UUID, r.Version)
		if err != nil {
			return nil, err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			continue
		}
		r.Status, r.Paragraph, r.Error = status, text, failure
		updated = append(updated, r)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (h *Harness) completeBatch(records []Record, status string, paragraphs map[string]string, failure string) {
	updated, err := h.storeBatch(records, status, paragraphs, failure)
	if err != nil {
		for _, r := range records {
			r.Status, r.Error = "failed", "persist AI result: "+err.Error()
			updated = append(updated, r)
		}
	}
	for _, r := range updated {
		select {
		case h.updates <- r:
		case <-h.ctx.Done():
		}
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
		key := taskKey(r.UUID, r.Version)
		if task := h.tasks[key]; task != nil {
			delete(h.tasks, key)
			task.remaining--
			if task.remaining == 0 {
				task.cancel()
			}
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
