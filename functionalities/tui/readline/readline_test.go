package readline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"st-go/tui/pinyin"
)

func TestEditing(t *testing.T) {
	line := New("/α世界", 1, 100)
	line.Handle("<Left>")
	line.Handle("<BS>")
	if line.Text() != "/α界" || line.Cursor != 2 {
		t.Fatal("rune-aware deletion")
	}
	line.Handle("\x01")
	line.Handle("X")
	line.Handle("\x05")
	line.Handle("!")
	if line.Text() != "/Xα界!" {
		t.Fatalf("readline start/end: %q", line.Text())
	}
	line.Reset(":one two", 1)
	line.Handle("\x17")
	line.Handle("\x19")
	if line.Text() != ":one two" {
		t.Fatal("kill/yank")
	}
	line.Handle("\x01")
	line.Handle("<Delete>")
	if line.Text() != ":ne two" {
		t.Fatal("forward delete")
	}
}
func TestHistoryAndDraft(t *testing.T) {
	line := New("/draft", 1, 100)
	line.SetHistory([]string{":command", "/first", "/second"})
	line.Handle("<Left>")
	cursor := line.Cursor
	line.Handle("<Up>")
	line.Handle("<Up>")
	if line.Text() != "/first" {
		t.Fatal("history prefix")
	}
	line.Handle("<Down>")
	line.Handle("<Down>")
	if line.Text() != "/draft" || line.Cursor != cursor {
		t.Fatal("draft restoration")
	}
}
func TestBoundsPasteAndUTF8(t *testing.T) {
	line := New("", 0, 20)
	for _, b := range []byte("世界") {
		if result := line.FeedByte(b); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if line.Text() != "世界" {
		t.Fatal("fragmented input")
	}
	line.Insert("\r\na\tb")
	if line.Text() != "世界 a b" {
		t.Fatal("single-line paste")
	}
	before := line.Text()
	if err := line.Insert(strings.Repeat("x", 30)); err == nil || line.Text() != before {
		t.Fatal("size rejection was not atomic")
	}
	if err := line.Insert("\x1b:q!"); err == nil {
		t.Fatal("control injection")
	}
}
func TestHorizontalView(t *testing.T) {
	line := New("/"+strings.Repeat("世界", 20), 1, 1000)
	text, cursor := line.View(12, nil)
	if cursor >= 12 || line.ViewStart() <= 1 || !strings.HasPrefix(text, "/") || !utf8.ValidString(text) {
		t.Fatal("horizontal scroll/cursor bounds")
	}
	line.Handle("<Home>")
	_, cursor = line.View(12, nil)
	if cursor != 1 || line.ViewStart() != 1 {
		t.Fatal("home remained scrolled")
	}
}

func typeKeys(line *Line, keys string) Result {
	var result Result
	for _, key := range keys {
		result = line.Handle(string(key))
	}
	return result
}

func awaitLine(t *testing.T, line *Line, ready func() bool) Result {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var combined Result
	for !ready() {
		mergeResult(&combined, line.Poll())
		if combined.Err != nil {
			t.Fatal(combined.Err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for Pinyin: text=%q active=%v display=%+v", line.Text(), line.IMEActive(), line.ViewDisplay(80, nil))
		}
		time.Sleep(time.Millisecond)
	}
	return combined
}

func TestPinyinSelectionNormalExitAndEscape(t *testing.T) {
	line := New("", 0, 100)
	typeKeys(line, "[[[nh++3]]]")
	awaitLine(t, line, func() bool { return !line.IMEActive() })
	if line.IMEActive() || line.Text() != "浓厚" {
		t.Fatalf("selected normal exit = %q active=%v", line.Text(), line.IMEActive())
	}

	line.Reset("prefix ", 0)
	typeKeys(line, "[[[nh")
	result := line.Handle("<Esc>")
	if result.Cancel || line.IMEActive() || line.Text() != "prefix [[[nh" {
		t.Fatalf("IME escape = %+v text=%q active=%v", result, line.Text(), line.IMEActive())
	}
	if result := line.Handle("<Esc>"); !result.Cancel {
		t.Fatal("second Escape did not retain ordinary readline cancellation")
	}

	line.Reset("", 0)
	typeKeys(line, "[[[nihao]]]")
	if line.Text() != "nihao" {
		t.Fatalf("literal normal exit = %q", line.Text())
	}
}

func TestPinyinCandidatesAreTransientAfterCursor(t *testing.T) {
	line := New("tail", 0, 100)
	line.Handle("<Home>")
	typeKeys(line, "[[[nh")
	awaitLine(t, line, func() bool { return strings.Contains(line.candidateText(), "你好") })
	display := line.ViewDisplay(80, nil)
	if display.Before != "[[[nh" || display.After != "tail" {
		t.Fatalf("display split = %+v", display)
	}
	if !strings.Contains(display.Candidates, "1:你好") || !strings.Contains(display.Candidates, "5:男孩") {
		t.Fatalf("candidate display = %q", display.Candidates)
	}
	if line.Text() != "[[[nhtail" {
		t.Fatalf("candidates changed line = %q", line.Text())
	}
	line.Handle("1")
	if line.Text() != "[[[你好tail" || line.ViewDisplay(80, nil).Candidates != "" {
		t.Fatalf("selection = %q", line.Text())
	}
}

type testPinyinService struct {
	lookup func(context.Context, pinyin.Request) (pinyin.Response, error)
	exited chan struct{}
	once   sync.Once
}

func (service *testPinyinService) Lookup(ctx context.Context, request pinyin.Request) (pinyin.Response, error) {
	return service.lookup(ctx, request)
}
func (service *testPinyinService) Exited() <-chan struct{} { return service.exited }
func (service *testPinyinService) Close() error {
	service.once.Do(func() { close(service.exited) })
	return nil
}

func testWorker(lookup func(context.Context, pinyin.Request) (pinyin.Response, error)) *pinyin.Worker {
	return pinyin.NewWorker(func(context.Context) (pinyin.Service, error) {
		return &testPinyinService{lookup: lookup, exited: make(chan struct{})}, nil
	})
}

func testResponse(request pinyin.Request, prefix string) pinyin.Response {
	response := pinyin.Response{ID: request.ID, Prompt: request.Prompt, Total: pinyin.PageSize}
	for index := 1; index <= pinyin.PageSize; index++ {
		response.Candidates = append(response.Candidates, fmt.Sprintf("%s%d", prefix, index))
	}
	return response
}

func TestPinyinLookupIsAsynchronousAndStaleRepliesAreIgnored(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	line := New("", 0, 100)
	line.newPinyinWorker = func() *pinyin.Worker {
		return testWorker(func(ctx context.Context, request pinyin.Request) (pinyin.Response, error) {
			if request.Prompt == "n" {
				close(firstStarted)
				select {
				case <-releaseFirst:
				case <-ctx.Done():
					return pinyin.Response{}, ctx.Err()
				}
				return testResponse(request, "old"), nil
			}
			return testResponse(request, "new"), nil
		})
	}
	typeKeys(line, "[[[n")
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first lookup did not start")
	}
	started := time.Now()
	line.Handle("h")
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("Handle blocked on decoder for %v", elapsed)
	}
	if display := line.ViewDisplay(80, nil); display.Candidates != "" {
		t.Fatalf("stale candidates visible before release: %q", display.Candidates)
	}
	close(releaseFirst)
	awaitLine(t, line, func() bool { return strings.Contains(line.candidateText(), "new1") })
	if candidates := line.candidateText(); strings.Contains(candidates, "old") || !strings.Contains(candidates, "new5") {
		t.Fatalf("stale response was applied: %q", candidates)
	}
	line.Close()
}

func TestPinyinFailureRetainsTextAndQueuedInput(t *testing.T) {
	gate := make(chan struct{})
	line := New("", 0, 100)
	line.newPinyinWorker = func() *pinyin.Worker {
		return testWorker(func(ctx context.Context, request pinyin.Request) (pinyin.Response, error) {
			select {
			case <-gate:
				return pinyin.Response{}, fmt.Errorf("decoder failed")
			case <-ctx.Done():
				return pinyin.Response{}, ctx.Err()
			}
		})
	}
	typeKeys(line, "[[[nh1!")
	close(gate)
	deadline := time.Now().Add(time.Second)
	var result Result
	for result.Err == nil && time.Now().Before(deadline) {
		result = line.Poll()
		time.Sleep(time.Millisecond)
	}
	if result.Err == nil || line.IMEActive() || line.Text() != "[[[nh!" {
		t.Fatalf("failure result=%+v text=%q active=%v", result, line.Text(), line.IMEActive())
	}
}

func TestPinyinNarrowDisplayRemainsBounded(t *testing.T) {
	line := New("tail", 0, 100)
	line.Handle("<Home>")
	typeKeys(line, "[[[nh")
	awaitLine(t, line, func() bool { return line.candidateText() != "" })
	for cells := 1; cells <= 12; cells++ {
		display := line.ViewDisplay(cells, nil)
		shown := display.Before + display.Candidates + display.After
		used := 0
		for _, character := range shown {
			used += CellWidth(character, used)
		}
		if used > cells || display.Cursor >= cells {
			t.Fatalf("%d cells: width=%d display=%+v", cells, used, display)
		}
	}
	line.Close()

	line = New("Search: value", len("Search: "), 100)
	display := line.ViewDisplay(4, nil)
	if display.Before != "Sear" || display.Candidates != "" || display.After != "" {
		t.Fatalf("narrow protected prefix escaped bounds: %+v", display)
	}
}

func TestPinyinCandidatesRemainVisibleOnLongScrolledLine(t *testing.T) {
	line := New(strings.Repeat("x", 60), 0, 200)
	typeKeys(line, "[[[nh")
	awaitLine(t, line, func() bool { return line.candidateText() != "" })
	display := line.ViewDisplay(20, nil)
	if !strings.Contains(display.Candidates, "1:你") {
		t.Fatalf("candidate hidden at scrolled cursor: %+v", display)
	}
	shown := display.Before + display.Candidates + display.After
	used := 0
	for _, character := range shown {
		used += CellWidth(character, used)
	}
	if used > 20 {
		t.Fatalf("scrolled display width=%d: %+v", used, display)
	}
	line.Close()
}

func TestPinyinMaximumPromptAndDisable(t *testing.T) {
	line := New("", 0, 100)
	result := typeKeys(line, "[[["+strings.Repeat("a", 33))
	if result.Err == nil || line.IMEActive() || !strings.HasSuffix(line.Text(), strings.Repeat("a", 33)) {
		t.Fatalf("maximum prompt result=%+v text=%q", result, line.Text())
	}
	line = New("", 0, 100)
	line.DisableIME = true
	typeKeys(line, "[[[nh1]]]")
	if line.IMEActive() || line.Text() != "[[[nh1]]]" {
		t.Fatalf("disabled IME intercepted %q", line.Text())
	}
}
