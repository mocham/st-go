package aiharness

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func batchFixture() BatchRequest {
	ids := []string{UUID(), UUID(), UUID()}
	var document strings.Builder
	document.WriteString("CURRENT_DOCUMENT_SENTINEL\n")
	for _, id := range ids {
		document.WriteString(BlockText(id, Slot) + "\n")
	}
	return BatchRequest{UUIDs: ids, Document: "paper.tex", Context: "EXPLICIT_CONTEXT_SENTINEL", CurrentDocument: document.String(), Model: &Model{"test", "paragraph", "deep"}}
}

func batchReply(ids []string, prefix string) string {
	values := make(map[string]string)
	for i, id := range ids {
		values[id] = fmt.Sprintf("%s %d: 文 and \\textbf{source}.\nNext line.", prefix, i+1)
	}
	data, _ := json.Marshal(values)
	return string(data)
}

func TestBatchOneGenerationAtomicReadyAndVersionRecovery(t *testing.T) {
	request := batchFixture()
	ids := append([]string(nil), request.UUIDs...)
	var calls atomic.Int32
	started, release := make(chan string, 1), make(chan struct{})
	generator := generateFunc(func(ctx context.Context, prompt string, model *Model) (string, error) {
		if model.ID != "paragraph" {
			t.Error("batch model changed after submission")
		}
		if calls.Add(1) == 1 {
			started <- prompt
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return batchReply(ids, "Original"), nil
		}
		return batchReply(ids[1:2], "Revised"), nil
	})
	path := filepath.Join(t.TempDir(), "project.db")
	h, err := Open(path, generator)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	records, err := h.SubmitBatch(request)
	if err != nil {
		t.Fatal(err)
	}
	request.UUIDs[0], request.Model.ID, request.Context = UUID(), "mutated", "changed"
	var prompt string
	select {
	case prompt = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not start")
	}
	targets, _ := json.Marshal(ids)
	if !strings.Contains(prompt, string(targets)) || strings.Count(prompt, "EXPLICIT_CONTEXT_SENTINEL") != 1 || strings.Count(prompt, "CURRENT_DOCUMENT_SENTINEL") != 1 {
		t.Fatal("batch did not freeze one shared input and ordered target list")
	}
	close(release)
	first := awaitResult(t, h)
	states, err := h.States(request.Document)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if states[id] != "ready" {
			t.Fatal("partial batch advertised before all entries were persisted")
		}
	}
	want, _ := ParseInsertions(batchReply(ids, "Original"), ids)
	if first.Paragraph != want[first.UUID] {
		t.Fatal("wrong UUID-to-insertion mapping")
	}
	for range 2 {
		r := awaitResult(t, h)
		if r.Paragraph != want[r.UUID] {
			t.Fatal("wrong UUID-to-insertion mapping")
		}
	}
	for _, r := range records {
		latest, err := h.Latest(r.UUID)
		if err != nil || latest.Version != 1 || latest.Prompt != prompt || latest.Context != "EXPLICIT_CONTEXT_SENTINEL" {
			t.Fatalf("snapshot: %+v %v", latest, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("sent %d generations for one batch", calls.Load())
	}
	if err := h.RecordAction(records[1], "discarded", Slot); err != nil {
		t.Fatal(err)
	}
	request.UUIDs, request.Model.ID = ids[1:2], "paragraph"
	if _, err := h.SubmitBatch(request); err != nil {
		t.Fatal(err)
	}
	revised := awaitResult(t, h)
	if revised.UUID != ids[1] || revised.Version != 2 || !strings.HasPrefix(revised.Paragraph, "Revised") || calls.Load() != 2 {
		t.Fatal("explicit retry did not create one new version/request")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for i, id := range ids {
		r, err := h.Latest(id)
		version := 1
		if i == 1 {
			version = 2
		}
		if err != nil || r.Version != version || r.Status != "ready" {
			t.Fatalf("batch recovery: %+v %v", r, err)
		}
	}
	var previous string
	if err := h.db.QueryRow("SELECT paragraph FROM govim_ai_versions WHERE uuid=? AND version=1", ids[1]).Scan(&previous); err != nil || previous != want[ids[1]] {
		t.Fatal("old insertion overwritten")
	}
}

func TestBatchReservationIsAllOrNothing(t *testing.T) {
	request := batchFixture()
	var calls atomic.Int32
	h, err := Open(filepath.Join(t.TempDir(), "project.db"), generateFunc(func(context.Context, string, *Model) (string, error) {
		calls.Add(1)
		return batchReply(request.UUIDs, "Paragraph"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	// Fail after the first insert to exercise real transaction rollback.
	_, err = h.db.Exec(fmt.Sprintf(`CREATE TRIGGER reject_target BEFORE INSERT ON govim_ai_versions WHEN NEW.uuid='%s' BEGIN SELECT RAISE(ABORT,'test failure'); END`, request.UUIDs[1]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.SubmitBatch(request); err == nil {
		t.Fatal("failed reservation succeeded")
	}
	var count int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM govim_ai_versions").Scan(&count); err != nil || count != 0 || calls.Load() != 0 {
		t.Fatal("partial batch was persisted or dispatched")
	}
	if _, err := h.db.Exec("DROP TRIGGER reject_target"); err != nil {
		t.Fatal(err)
	}
	request.UUIDs[2] = request.UUIDs[0]
	if _, err := h.SubmitBatch(request); err == nil || calls.Load() != 0 {
		t.Fatal("duplicate target batch accepted")
	}
}

func TestDiscardOneBatchSlotKeepsSiblingsAndProtectsNewVersion(t *testing.T) {
	request := batchFixture()
	ids := append([]string(nil), request.UUIDs...)
	started, release := make(chan context.Context, 1), make(chan struct{})
	var calls atomic.Int32
	h, err := Open(filepath.Join(t.TempDir(), "project.db"), generateFunc(func(ctx context.Context, _ string, _ *Model) (string, error) {
		if calls.Add(1) == 1 {
			started <- ctx
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return batchReply(ids, "Old"), nil
		}
		return batchReply(ids[:1], "New"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	records, err := h.SubmitBatch(request)
	if err != nil {
		t.Fatal(err)
	}
	var ctx context.Context
	select {
	case ctx = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not start")
	}
	if err := h.RecordAction(records[0], "discarded", Slot); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil || h.Active(records[0]) || !h.Active(records[1]) {
		t.Fatal("discard cancelled surviving slots")
	}
	request.UUIDs = ids[:1]
	if _, err := h.SubmitBatch(request); err != nil {
		t.Fatal(err)
	}
	close(release)
	for range 3 {
		awaitResult(t, h)
	}
	for i, id := range ids {
		r, err := h.Latest(id)
		want, version := "Old", 1
		if i == 0 {
			want, version = "New", 2
		}
		if err != nil || r.Status != "ready" || r.Version != version || !strings.HasPrefix(r.Paragraph, want) {
			t.Fatalf("late batch changed wrong version: %+v %v", r, err)
		}
	}
	var status, paragraph string
	if err := h.db.QueryRow("SELECT status,paragraph FROM govim_ai_versions WHERE uuid=? AND version=1", ids[0]).Scan(&status, &paragraph); err != nil || status != "discarded" || paragraph != "" {
		t.Fatal("discarded result revived")
	}
	if calls.Load() != 2 {
		t.Fatal("batch was split or retried")
	}
}

func TestDiscardAllBatchSlotsCancelsSharedRequest(t *testing.T) {
	request := batchFixture()
	started := make(chan context.Context, 1)
	h, err := Open(filepath.Join(t.TempDir(), "project.db"), generateFunc(func(ctx context.Context, _ string, _ *Model) (string, error) {
		started <- ctx
		<-ctx.Done()
		return batchReply(request.UUIDs, "Late"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	records, err := h.SubmitBatch(request)
	if err != nil {
		t.Fatal(err)
	}
	var ctx context.Context
	select {
	case ctx = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not start")
	}
	for i, r := range records {
		if err := h.RecordAction(r, "discarded", Slot); err != nil {
			t.Fatal(err)
		}
		if i < len(records)-1 && ctx.Err() != nil {
			t.Fatal("shared request cancelled too early")
		}
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("fully discarded batch kept running")
	}
	h.wg.Wait()
	for _, r := range records {
		latest, err := h.Latest(r.UUID)
		if err != nil || latest.Status != "discarded" || latest.Paragraph != "" {
			t.Fatal("late completion revived discarded slot")
		}
	}
}

func TestMalformedBatchFailsAllSlotsWithoutRetry(t *testing.T) {
	request := batchFixture()
	var calls atomic.Int32
	h, err := Open(filepath.Join(t.TempDir(), "project.db"), generateFunc(func(context.Context, string, *Model) (string, error) {
		calls.Add(1)
		return batchReply(request.UUIDs[:2], "Incomplete"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := h.SubmitBatch(request); err != nil {
		t.Fatal(err)
	}
	first := awaitResult(t, h)
	if first.Status != "failed" || !strings.Contains(first.Error, "missing") {
		t.Fatalf("invalid batch state: %+v", first)
	}
	for _, id := range request.UUIDs {
		r, err := h.Latest(id)
		if err != nil || r.Status != "failed" || r.Paragraph != "" {
			t.Fatal("malformed batch partially published")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("malformed batch automatically retried")
	}
}

func TestInsertionJSONRejectsAmbiguousOrInvalidMappings(t *testing.T) {
	ids := []string{UUID(), UUID()}
	good := batchReply(ids, "Valid")
	values, err := ParseInsertions(good, ids)
	if err != nil || !strings.Contains(values[ids[0]], `\textbf{source}`) || !strings.Contains(values[ids[1]], "\nNext line.") {
		t.Fatalf("JSON escaping lost: %v", err)
	}
	for name, response := range map[string]string{
		"missing":        batchReply(ids[:1], "Missing"),
		"unknown":        batchReply(append(append([]string(nil), ids...), UUID()), "Unknown"),
		"duplicate":      fmt.Sprintf(`{%q:"one",%q:"two",%q:"three"}`, ids[0], ids[0], ids[1]),
		"non-string":     fmt.Sprintf(`{%q:1,%q:"two"}`, ids[0], ids[1]),
		"null":           fmt.Sprintf(`{%q:null,%q:"two"}`, ids[0], ids[1]),
		"empty":          fmt.Sprintf(`{%q:" ",%q:"two"}`, ids[0], ids[1]),
		"markers":        fmt.Sprintf(`{%q:"<++>",%q:"two"}`, ids[0], ids[1]),
		"braces":         fmt.Sprintf(`{%q:"unmatched }",%q:"two"}`, ids[0], ids[1]),
		"truncated":      good[:len(good)-1],
		"extra-document": good + " {}",
		"fenced":         "```json\n" + good + "\n```",
		"array":          "[]",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseInsertions(response, ids); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}
