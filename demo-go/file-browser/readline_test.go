package main

import "testing"

func TestSharedReadlinePromptUnicodeAndCursor(t *testing.T) {
	b := &Browser{promptMode: "cmd", promptBuffer: []byte(":α世界")}
	b.promptEdit("<Left>")
	b.promptBackspace()
	if string(b.promptBuffer) != ":α界" {
		t.Fatalf("Unicode backspace=%q", b.promptBuffer)
	}
	b.handlePromptKey(1)
	b.promptInsert([]byte("X"))
	b.handlePromptKey(5)
	b.promptInsert([]byte("!"))
	if string(b.promptBuffer) != "X:α界!" {
		t.Fatalf("readline begin/end=%q", b.promptBuffer)
	}
	for _, c := range []byte(" café") {
		b.handlePromptKey(c)
	}
	if string(b.promptBuffer) != "X:α界! café" {
		t.Fatal("fragmented UTF-8 input")
	}
}
