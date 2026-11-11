package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"st-go/tui/pinyin"
)

type cjkTestService struct {
	gate   <-chan struct{}
	exited chan struct{}
	once   sync.Once
}

func (s *cjkTestService) Lookup(ctx context.Context, r pinyin.Request) (pinyin.Response, error) {
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return pinyin.Response{}, ctx.Err()
		}
	}
	page := min(r.Page, 5)
	response := pinyin.Response{ID: r.ID, Prompt: r.Prompt, Page: page, Total: 30}
	for i := 0; i < 5; i++ {
		response.Candidates = append(response.Candidates, fmt.Sprintf("候选%d", page*5+i+1))
	}
	if page == 2 {
		response.Candidates[2] = "浓厚"
	}
	return response, nil
}
func (s *cjkTestService) Exited() <-chan struct{} { return s.exited }
func (s *cjkTestService) Close() error            { s.once.Do(func() { close(s.exited) }); return nil }
func fakeCJK(e *Editor, gate <-chan struct{}) {
	e.newPinyinWorker = func() *pinyin.Worker {
		return pinyin.NewWorker(func(context.Context) (pinyin.Service, error) {
			return &cjkTestService{gate: gate, exited: make(chan struct{})}, nil
		})
	}
}
func awaitCJK(t *testing.T, e *Editor, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e.tick(time.Now())
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("CJK did not settle: text=%q message=%q mode=%v", editorText(e), e.message, e.mode)
}

func TestCJKFastExampleExitUndoRedoAndRepeat(t *testing.T) {
	e := memoryEditor("\n")
	defer e.close(false)
	gate := make(chan struct{})
	fakeCJK(e, gate)
	editKeys(e, "i[[[nh++3]]]<Esc>")
	if e.cjk == nil || e.cjk.selection != 3 || editorText(e) != "[[[nh\n" {
		t.Fatalf("pending composition lost input: %q", editorText(e))
	}
	close(gate)
	awaitCJK(t, e, func() bool { return e.cjk == nil && e.mode == normalMode })
	if got := editorText(e); got != "浓厚\n" {
		t.Fatalf("example = %q", got)
	}
	editKeys(e, "u")
	if got := editorText(e); got != "\n" {
		t.Fatalf("CJK insert not one undo group: %q", got)
	}
	editKeys(e, "<C-R>")
	if got := editorText(e); got != "浓厚\n" {
		t.Fatalf("redo = %q", got)
	}
	editKeys(e, "0.")
	if strings.Count(editorText(e), "浓厚") != 2 {
		t.Fatalf("dot reran asynchronous IME rather than accepted Unicode: %q", editorText(e))
	}
	if strings.Contains(editorText(e), "[[[") {
		t.Fatal("normal-exit delimiters leaked into repeat")
	}
}

func TestCJKBackspacePreviousPageLiteralPasteAndLimit(t *testing.T) {
	e := memoryEditor("\n")
	defer e.close(false)
	fakeCJK(e, nil)
	editKeys(e, "i[[[nhx<BS>++-")
	awaitCJK(t, e, func() bool { return e.cjk != nil && e.cjk.ready })
	if e.cjk.prompt != "nh" || e.cjk.page != 1 {
		t.Fatalf("composition/page = %+v", e.cjk)
	}
	editKeys(e, "1]]]")
	if e.cjk != nil || !strings.Contains(editorText(e), "候选6") {
		t.Fatalf("selection/exit = %q", editorText(e))
	}
	e.Feed(keyEvent{Key: "<Paste>", Paste: "[[[literal]]]"})
	if e.cjk != nil {
		t.Fatal("pasted delimiters activated IME")
	}
	editKeys(e, "[[["+strings.Repeat("a", pinyin.MaxPrompt+1))
	if e.cjk != nil || !strings.Contains(e.message, "exceeds") || !strings.Contains(editorText(e), strings.Repeat("a", pinyin.MaxPrompt+1)) {
		t.Fatal("prompt limit dropped typed text or left IME active")
	}
}

func TestCJKMacroReplaysAcceptedUnicodeWithoutDecoder(t *testing.T) {
	e := memoryEditor("\n")
	defer e.close(false)
	fakeCJK(e, nil)
	editKeys(e, "qai[[[nh++3]]]<Esc>")
	awaitCJK(t, e, func() bool { return e.cjk == nil && e.mode == normalMode })
	editKeys(e, "q")
	e.newPinyinWorker = func() *pinyin.Worker {
		return pinyin.NewWorker(func(context.Context) (pinyin.Service, error) {
			return nil, fmt.Errorf("decoder unavailable during replay")
		})
	}
	editKeys(e, "0@a")
	awaitCJK(t, e, func() bool { return e.cjk == nil && e.mode == normalMode })
	if strings.Count(editorText(e), "浓厚") != 2 {
		t.Fatalf("macro depended on an asynchronous decoder: %q", editorText(e))
	}
}

func TestCJKResultPaintsOnlyCommandRow(t *testing.T) {
	e := memoryEditor("unchanged body\nsecond line\n")
	defer e.close(false)
	gate := make(chan struct{})
	fakeCJK(e, gate)
	editKeys(e, "i[[[nh")
	var r renderer
	screen := newPaintTerminal(e.cols, e.rows)
	screen.Twrite([]byte("\x1b[?7l"), false)
	var out strings.Builder
	if err := e.draw(&r, &out); err != nil {
		t.Fatal(err)
	}
	emittedPaintRows(t, screen, out.String())
	close(gate)
	awaitCJK(t, e, func() bool { return e.cjk != nil && e.cjk.ready })
	out.Reset()
	if err := e.draw(&r, &out); err != nil {
		t.Fatal(err)
	}
	rows := emittedPaintRows(t, screen, out.String())
	if len(rows) != 1 || !rows[e.rows-1] {
		t.Fatalf("candidate result repainted document rows: %+v output=%q", rows, out.String())
	}
	if status := e.cjkStatus(e.cols); !strings.Contains(status, "1:") || !strings.Contains(status, "5:") {
		t.Fatalf("candidate bar does not show five choices: %q", status)
	}
	if status := truncateCells(e.cjkStatus(32), 31); strings.Count(status, "候") != 5 || !strings.Contains(status, "5") {
		t.Fatalf("narrow preview-split command bar hid Chinese candidates: %q", status)
	}
}

func TestCJKServiceFailureRetainsSpellingAndQueuedKeys(t *testing.T) {
	e := memoryEditor("\n")
	defer e.close(false)
	gate := make(chan struct{})
	e.newPinyinWorker = func() *pinyin.Worker {
		return pinyin.NewWorker(func(ctx context.Context) (pinyin.Service, error) {
			select {
			case <-gate:
				return nil, fmt.Errorf("service crashed")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
	}
	editKeys(e, "i[[[nh1after]]]<Esc>")
	close(gate)
	awaitCJK(t, e, func() bool { return e.cjk == nil && e.mode == normalMode })
	if got := editorText(e); got != "[[[nhafter]]]\n" || !strings.Contains(e.message, "CJK stopped") {
		t.Fatalf("crash lost spelling/queued input: %q message=%q", got, e.message)
	}
}

func TestCJKAcceptedTextIsJournaledAndRecoverable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e := newEditor(defaultJournalOptions())
	defer e.close(false)
	if _, err := e.open(path, false); err != nil {
		t.Fatal(err)
	}
	fakeCJK(e, nil)
	editKeys(e, "i[[[nh++3]]]<Esc>")
	awaitCJK(t, e, func() bool { return e.cjk == nil && e.mode == normalMode })
	if err := e.close(false); err != nil {
		t.Fatal(err)
	}
	recovered := newEditor(defaultJournalOptions())
	defer recovered.close(true)
	if _, err := recovered.open(path, true); err != nil {
		t.Fatal(err)
	}
	if got := editorText(recovered); got != "浓厚\n" {
		t.Fatalf("recovery = %q", got)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "\n" {
		t.Fatal("IME implicitly saved the source file")
	}
}

func cjkBuiltHelper(t *testing.T) string {
	t.Helper()
	path := os.Getenv("GOVIM_PINYIN_HELPER")
	if path == "" {
		path = "../../BINARIES/govim-pinyind"
	}
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("run make govim-pinyind for helper integration")
	}
	return path
}
func TestPTYCJKRealGooglePinyinExample(t *testing.T) {
	t.Setenv("GOVIM_PINYIN_HELPER", cjkBuiltHelper(t))
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := startPTYEditor(t, "--no-preview", path)
	p.send(t, "i[[[nh++3]]]")
	p.await(t, "real Chinese candidate committed", func() bool {
		return strings.Contains(p.screenText(), "浓厚") && !strings.Contains(p.screenText(), "[[[")
	})
	p.send(t, "\x1b")
	time.Sleep(100 * time.Millisecond)
	p.send(t, ":wq\r")
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("CJK editor did not quit")
	}
	if p.err != nil {
		t.Fatalf("editor exit: %v", p.err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "浓厚\n" {
		t.Fatalf("saved Chinese = %q %v", data, err)
	}
}

func TestCJKNormalExitRemovesDelimitersButEscapePreservesThem(t *testing.T) {
	for _, test := range []struct{ keys, want string }{
		{"i[[[nh++3]]]<Esc>", "浓厚\n"},
		{"i[[[nh++3<Esc>", "[[[浓厚\n"},
		{"i[[[nh<Esc>", "[[[nh\n"},
		{"i[[[nh]]]<Esc>", "nh\n"},
		{"3i[[[nh++3]]]<Esc>", "浓厚浓厚浓厚\n"},
	} {
		t.Run(test.keys, func(t *testing.T) {
			e := memoryEditor("\n")
			defer e.close(false)
			fakeCJK(e, nil)
			editKeys(e, test.keys)
			awaitCJK(t, e, func() bool { return e.cjk == nil && e.mode == normalMode })
			if got := editorText(e); got != test.want {
				t.Fatalf("text=%q want %q", got, test.want)
			}
		})
	}
}

func TestCJKUnavailableHelperDoesNotActivate(t *testing.T) {
	for _, mode := range []string{"missing", "not-executable", "bad-executable"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "helper")
			if mode != "missing" {
				permission := os.FileMode(0600)
				if mode == "bad-executable" {
					permission = 0700
				}
				if err := os.WriteFile(path, []byte("not an executable"), permission); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("GOVIM_PINYIN_HELPER", path)
			e := memoryEditor("\n")
			defer e.close(false)
			editKeys(e, "i[[[")
			if e.cjk != nil || len(e.pinyinClosing) != 0 {
				t.Fatal("failed spawn activated CJK or started a worker")
			}
			editKeys(e, "nh++3]]]<Esc>")
			if got := editorText(e); got != "[[[nh++3]]]\n" {
				t.Fatalf("unavailable helper intercepted ordinary insertion: %q", got)
			}
		})
	}
}

func TestCJKSpawnsLazilyOnlyOnLiveTriple(t *testing.T) {
	e := memoryEditor("\n")
	defer e.close(false)
	starts := 0
	e.newPinyinWorker = func() *pinyin.Worker {
		starts++
		return pinyin.NewWorker(func(context.Context) (pinyin.Service, error) {
			return &cjkTestService{exited: make(chan struct{})}, nil
		})
	}
	e.tick(time.Now())
	var r renderer
	e.draw(&r, io.Discard)
	if starts != 0 {
		t.Fatal("editor startup spawned Pinyin")
	}
	editKeys(e, "i")
	e.Feed(keyEvent{Key: "<Paste>", Paste: "[[[literal]]]"})
	editKeys(e, "[[")
	if starts != 0 || e.cjk != nil {
		t.Fatal("paste/partial delimiter spawned Pinyin")
	}
	editKeys(e, "[")
	if starts != 1 || e.cjk == nil {
		t.Fatal("live triple did not lazily spawn Pinyin")
	}
}
func TestCJKPureGoMaximumPromptStaysAliveAndAllowsRestart(t *testing.T) {
	helper := cjkBuiltHelper(t)
	e := memoryEditor("\n")
	defer e.close(false)
	e.newPinyinWorker = func() *pinyin.Worker {
		return pinyin.NewWorker(func(ctx context.Context) (pinyin.Service, error) { return pinyin.Start(ctx, helper) })
	}
	prompt := strings.Repeat("a", pinyin.MaxPrompt)
	editKeys(e, "i[[["+prompt)
	awaitCJK(t, e, func() bool {
		return e.cjk != nil && e.cjk.ready && e.cjk.response.Prompt == prompt
	})
	if strings.Contains(e.message, "CJK stopped") || !strings.Contains(editorText(e), prompt) {
		t.Fatal("bounded pure-Go lookup stopped the helper or lost input")
	}
	editKeys(e, "<Esc>a ]]][[[nh++3]]]<Esc>")
	awaitCJK(t, e, func() bool { return e.mode == normalMode && e.cjk == nil })
	if !strings.Contains(editorText(e), "浓厚") {
		t.Fatalf("editor could not restart the pure-Go IME: %q", editorText(e))
	}
	var r renderer
	if err := e.draw(&r, io.Discard); err != nil {
		t.Fatal(err)
	}
}
