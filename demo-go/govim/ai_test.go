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
	e.aiProject = root
	t.Setenv("ST_GO_APPBUS_TOKEN", appbus.ID())
	if err := e.ensureAI(); err != nil {
		t.Fatal(err)
	}
	e.ai.bus.Out = io.Discard
	e.ai.provider = appbus.ID()
	t.Cleanup(func() { e.close(false) })
	return e
}
func deliverAI(t *testing.T, e *Editor, m writing.Message) {
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
		e.Feed(keyEvent{Bus: fmt.Sprintf("app-bus %s message %s %d %s %s %d %d %s", e.ai.bus.ID, e.ai.provider, i+1, e.ai.bus.Channel, id, i, count, base64.RawStdEncoding.EncodeToString(part))})
	}
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
