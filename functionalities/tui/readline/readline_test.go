package readline

import (
	"strings"
	"testing"
	"unicode/utf8"
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
