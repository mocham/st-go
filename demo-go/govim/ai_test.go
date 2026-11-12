package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"st-go/tui/appbus"
	"st-go/tui/writing"
)

func aiTestEditor(t *testing.T) *Editor {
	t.Helper()
	root := t.TempDir()
	text := "前文 paragraph.\n\nFollowing text.\n"
	path := filepath.Join(root, "paper.md")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	e := memoryEditor(text)
	e.buffer().meta.Path = path
	t.Chdir(root)
	t.Setenv("ST_GO_APPBUS_TOKEN", appbus.ID())
	if err := e.aiSubscribe("test-writing"); err != nil {
		t.Fatal(err)
	}
	e.ai.bus.Out = io.Discard
	e.ai.provider = appbus.ID()
	e.ai.bus.Ready = true
	e.ai.providers[e.ai.provider] = aiPeer{seen: time.Now(), project: root, session: "ses_writing"}
	t.Cleanup(func() { e.close(false) })
	return e
}
func deliverAI(t *testing.T, e *Editor, m writing.Message) {
	t.Helper()
	deliverAIFrom(t, e, e.ai.provider, m)
}

func deliverAIFrom(t *testing.T, e *Editor, sender string, m writing.Message) {
	t.Helper()
	m.Version = writing.Version
	m.To = e.ai.bus.ID
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	id := appbus.ID()
	count := (len(data) + appbus.ChunkBytes - 1) / appbus.ChunkBytes
	for i := 0; i < count; i++ {
		part := data[i*appbus.ChunkBytes : min(len(data), (i+1)*appbus.ChunkBytes)]
		e.Feed(keyEvent{Bus: fmt.Sprintf("app-bus %s message %s %d %s %s %d %d %s", e.ai.bus.ID, sender, i+1, e.ai.bus.Channel, id, i, count, base64.RawStdEncoding.EncodeToString(part))})
	}
}

func TestAISubscribeUsesWorkdirAndRefusesDisconnectedRequests(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	if err := os.MkdirAll(filepath.Join(root, "chapters"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "go.mod"), []byte("module parent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("ST_GO_APPBUS_TOKEN", appbus.ID())
	e := memoryEditor("Paragraph.\n")
	e.buffer().meta.Path = filepath.Join(root, "chapters", "paper.md")
	t.Cleanup(func() { e.close(false) })
	before := string(e.buffer().text)
	e.aiCommand("AINext", "")
	if e.ai != nil || !strings.Contains(e.message, ":Subscribe") || string(e.buffer().text) != before {
		t.Fatalf("request implicitly subscribed: %s", e.message)
	}
	e.aiCommand("Subscribe", "paper")
	if e.ai == nil || e.ai.store.root != root {
		t.Fatalf("subscription did not use workdir: %s", e.message)
	}
	e.ai.bus.Out = io.Discard
	if _, err := os.Stat(filepath.Join(root, ".govim", "ai.sqlite3")); err != nil {
		t.Fatal(err)
	}
	e.aiReceive("app-bus " + e.ai.bus.ID + " ready")
	if err := e.aiNext(""); err == nil || string(e.buffer().text) != before {
		t.Fatal("created a placeholder without a responder")
	}
	deliverAIFrom(t, e, appbus.ID(), writing.Message{Type: "ready", Project: parent})
	if e.ai.provider != "" || !strings.Contains(e.message, "workdir mismatch") {
		t.Fatalf("mismatched workdir silently ignored: %s", e.message)
	}
	provider := appbus.ID()
	deliverAIFrom(t, e, provider, writing.Message{Type: "ready", Project: root, Session: "ses_existing"})
	if err := e.aiNext(""); err != nil {
		t.Fatal(err)
	}
	p := e.ai.proposals[e.ai.focus]
	if p.Session != "ses_existing" {
		t.Fatal("request not bound to subscribed session")
	}
	e.aiTick(time.Now())
	if p.Status != "sent" {
		t.Fatalf("request not published: %s", p.Status)
	}
	e.aiTick(time.Now().Add(16 * time.Second))
	if p.Status != "unknown" || !strings.Contains(e.message, "not acknowledged") {
		t.Fatalf("request stayed queued forever: %s %s", p.Status, e.message)
	}
	// Reopening the existing server session reconciles this request, not a
	// second suggest. A delayed final response may still finish it safely.
	deliverAIFrom(t, e, provider, writing.Message{Type: "ready", Project: root, Session: "ses_existing"})
	statuses, suggests := 0, 0
	for _, m := range e.ai.outbox {
		if m.Type == "status" && m.RequestID == p.Request {
			statuses++
		}
		if m.Type == "suggest" {
			suggests++
		}
	}
	if statuses != 1 || suggests != 0 {
		t.Fatalf("incorrect recovery: %d statuses, %d suggests", statuses, suggests)
	}
	completeAI(t, e, p, "Recovered prediction.")
}
func completeAI(t *testing.T, e *Editor, p *aiProposal, prediction string) {
	begin, end := writing.Markers(p.Request)
	deliverAI(t, e, writing.Message{Type: "result", RequestID: p.Request, Placeholder: p.ID, Prediction: prediction, Reasoning: "Exposed provider reasoning.", Text: "Commentary\n" + begin + "\n" + prediction + "\n" + end, Session: "ses_writing"})
	if p.Status != "result" {
		t.Fatalf("completion rejected: %+v, %s", p, e.message)
	}
}

func TestAIPlaceholderAdaptInsertUndoAndImmutableHistory(t *testing.T) {
	e := aiTestEditor(t)
	source := e.buffer()
	e.setPos(source.start(1))
	if err := e.aiNext("Continue"); err != nil {
		t.Fatal(err)
	}
	p := e.ai.proposals[e.ai.focus]
	marker := writing.Marker(p.Document, p.ID)
	if strings.Count(string(source.text), marker) != 1 {
		t.Fatal("missing UUID marker")
	}
	request := e.ai.outbox[len(e.ai.outbox)-1]
	request.Version = writing.Version
	context, err := writing.ReadContext(request, e.ai.store.root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(context, "前文 paragraph.") {
		t.Fatal("unsaved request snapshot missing")
	}
	// Unrelated Unicode edits move the source marker while the request is running.
	e.replace(0, 0, "新增文字\n", 0)
	completeAI(t, e, p, "Original predicted paragraph.")
	if err := e.aiEdit(p); err != nil {
		t.Fatal(err)
	}
	e.replace(0, len(e.buffer().text), "Author adaptation.", 0)
	if err := e.aiSaveDraft(); err != nil {
		t.Fatal(err)
	}
	// Redelivery after transport recovery must not clobber the author's draft.
	completeAI(t, e, p, "Original predicted paragraph.")
	if p.Draft != "Author adaptation." {
		t.Fatal("redelivery overwrote adaptation")
	}
	if err := e.aiInsert(p); err != nil {
		t.Fatal(err)
	}
	if e.buffer() != source || strings.Contains(string(source.text), marker) || !strings.Contains(string(source.text), "Author adaptation.") {
		t.Fatal("wrong insertion destination/content")
	}
	if strings.Contains(string(source.text), "Commentary") || strings.Contains(string(source.text), "Reasoning") {
		t.Fatal("nonprediction inserted")
	}
	revision := source.syncRevision
	if _, err := source.undoChange(false); err != nil {
		t.Fatal(err)
	}
	if source.syncRevision <= revision || strings.Count(string(source.text), marker) != 1 {
		t.Fatal("undo was not one monotonic edit")
	}
	e.aiTick(time.Now().Add(time.Second))
	if p.Status != "result" {
		t.Fatalf("undo did not restore pending proposal: %s", p.Status)
	}
	var original string
	if err := e.ai.store.db.QueryRow("SELECT prediction FROM requests WHERE id=?", p.Request).Scan(&original); err != nil || original != "Original predicted paragraph." {
		t.Fatalf("original overwritten: %q %v", original, err)
	}
	var count int
	if err := e.ai.store.db.QueryRow("SELECT count(*) FROM adaptations WHERE placeholder=?", p.ID).Scan(&count); err != nil || count < 2 {
		t.Fatalf("missing edit/acceptance history: %d %v", count, err)
	}
	all, err := e.ai.store.all()
	if err != nil || len(all) != 1 || all[0].Draft != "Author adaptation." {
		t.Fatalf("durable history: %+v %v", all, err)
	}
}

func TestAICheckpointIsOfflineJournaledSourceEdit(t *testing.T) {
	for _, name := range []string{"paper.md", "paper.tex"} {
		t.Run(name, func(t *testing.T) {
			original := "Earlier paragraph.\n\n新文.\n"
			e := memoryEditor(original)
			e.buffer().meta.Path = filepath.Join(t.TempDir(), name)
			t.Cleanup(func() { e.close(false) })
			cursor := e.buffer().start(2)
			e.setPos(cursor)
			e.aiCommand("AICheckpoint", "")
			marker := writing.Checkpoint(name) + "\n"
			want := "Earlier paragraph.\n\n" + marker + "新文.\n"
			if e.ai != nil || previewText(e.buffer()) != want || e.pos() != cursor+utf8.RuneCountInString(marker) {
				t.Fatalf("bad checkpoint edit: %q %s", string(e.buffer().text), e.message)
			}
			e.aiCommand("AICheckpoint", "")
			if previewText(e.buffer()) != want {
				t.Fatal("repeated command duplicated checkpoint")
			}
			if _, err := e.buffer().undoChange(false); err != nil || previewText(e.buffer()) != original {
				t.Fatalf("checkpoint not one undo operation: %v", err)
			}
		})
	}
}

func TestAINextFreezesOriginalCursorAndRefinementUsesSourceAnchor(t *testing.T) {
	e := aiTestEditor(t)
	source := e.buffer()
	text := "# Excluded heading\nOld source.\n" + writing.Checkpoint("paper.md") + "\n新文 prefix tail of paragraph.\n\nFollowing source.\n"
	e.replace(0, len(source.text), text, 0)
	cursor := utf8.RuneCountInString(strings.Split(text, "tail of paragraph")[0])
	e.setPos(cursor)
	if err := e.aiNext(""); err != nil {
		t.Fatal(err)
	}
	p := e.ai.proposals[e.ai.focus]
	request := e.ai.outbox[len(e.ai.outbox)-1]
	request.Version = writing.Version
	context, err := writing.ReadContext(request, e.ai.store.root)
	if err != nil || !strings.Contains(context, "新文 prefix ") {
		t.Fatalf("missing selected source: %v\n%s", err, context)
	}
	for _, excluded := range []string{"Excluded heading", "Old source", "tail of paragraph", "Following source"} {
		if strings.Contains(context, excluded) {
			t.Fatalf("source outside cursor range was sent: %s", excluded)
		}
	}
	data, err := os.ReadFile(p.Manifest)
	var manifest writing.Manifest
	if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.CursorRune != cursor || manifest.Context == nil {
		t.Fatalf("original cursor/range not recorded: %s %v", data, err)
	}
	// Later unrelated editing must not change the request's frozen context.
	e.replace(0, 0, "An unrelated edit.\n", 0)
	if again, err := writing.ReadContext(request, e.ai.store.root); err != nil || again != context {
		t.Fatal("later edits changed the submitted excerpt")
	}
	completeAI(t, e, p, "Draft paragraph.")
	if err := e.aiEdit(p); err != nil {
		t.Fatal(err)
	}
	e.setPos(0) // This draft cursor must never be used as a source offset.
	if err := e.aiSubmit(p, "Revise"); err != nil {
		t.Fatal(err)
	}
	request = e.ai.outbox[len(e.ai.outbox)-1]
	request.Version = writing.Version
	context, err = writing.ReadContext(request, e.ai.store.root)
	if err != nil || !strings.Contains(context, "新文 prefix tail of paragraph.") || strings.Contains(context, "Following source") || strings.Contains(context, "An unrelated edit") {
		t.Fatalf("refinement used wrong source bounds: %v\n%s", err, context)
	}
}

func TestAIInsAwayFromMarkerUsesPickerAndDuplicateMarkerRefused(t *testing.T) {
	e := aiTestEditor(t)
	if err := e.aiNext(""); err != nil {
		t.Fatal(err)
	}
	p := e.ai.proposals[e.ai.focus]
	completeAI(t, e, p, "Prediction.")
	e.setPos(0)
	e.aiCommand("AIIns", "")
	if len(e.ai.picker) != 1 || p.Status != "result" {
		t.Fatal("AIIns silently selected focus instead of picker")
	}
	e.Feed(keyEvent{Key: "<Esc>"})
	e.aiCommand("AIIns", "")
	e.Feed(keyEvent{Key: "<Enter>"})
	if p.Status != "inserted" {
		t.Fatal("picker Enter did not insert")
	}
	if _, err := e.buffer().undoChange(false); err != nil {
		t.Fatal(err)
	}
	e.aiTick(time.Now().Add(time.Second))
	e.replace(0, 0, writing.Marker(p.Document, p.ID)+"\n", 0)
	before := string(e.buffer().text)
	if err := e.aiInsert(p); err == nil || string(e.buffer().text) != before {
		t.Fatal("ambiguous placeholder inserted")
	}
}

func TestAIRegenerationRetainsOldResponsesWithoutReplacingNewProposal(t *testing.T) {
	e := aiTestEditor(t)
	if err := e.aiNext(""); err != nil {
		t.Fatal(err)
	}
	p := e.ai.proposals[e.ai.focus]
	old := p.Request
	if err := e.aiSubmit(p, "Try again"); err != nil {
		t.Fatal(err)
	}
	begin, end := writing.Markers(old)
	deliverAI(t, e, writing.Message{Type: "result", RequestID: old, Placeholder: p.ID, Text: begin + "\nOld response\n" + end, Prediction: "Old response"})
	if p.Status != "queued" || p.Draft != "" {
		t.Fatal("stale response replaced current generation")
	}
	var stored string
	if err := e.ai.store.db.QueryRow("SELECT prediction FROM requests WHERE id=?", old).Scan(&stored); err != nil || stored != "Old response" {
		t.Fatalf("old response lost: %q %v", stored, err)
	}
}
