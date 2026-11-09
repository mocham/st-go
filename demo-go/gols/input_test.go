package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestFragmentedPDFSearchRepliesAreNotKeys(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	var replies []string
	in := &input{fd: int(reader.Fd()), reply: func(payload string) { replies = append(replies, payload) }}
	for _, fragment := range []string{"\x1bPpdfsearch data 1 ", "0 -1 e30=\x1b", "\\"} {
		writer.Write([]byte(fragment))
		if _, status := in.readByte(time.Millisecond); status != inTimeout {
			t.Fatal("fragmented reply leaked a key")
		}
	}
	writer.Write([]byte("n\x1b[B"))
	for _, want := range []byte("n\x1b[B") {
		if got, status := in.readByte(time.Millisecond); status != inData || got != want {
			t.Fatalf("input=%q,%d,want%q", got, status, want)
		}
	}
	if len(replies) != 1 || replies[0] != "pdfsearch data 1 0 -1 e30=" {
		t.Fatalf("reply=%q", replies)
	}
	// Oversized DCS input is drained through ST with bounded retained storage.
	go func() { writer.Write([]byte("\x1bP" + strings.Repeat("x", 9000) + "\x1b\\q")) }()
	for {
		key, status := in.readByte(time.Millisecond)
		if status == inData {
			if key != 'q' {
				t.Fatal("oversized reply leaked into navigation")
			}
			break
		}
	}
	if len(replies) != 1 || len(in.frame) > 8192 {
		t.Fatal("oversized reply was retained or delivered")
	}
}
