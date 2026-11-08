package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFragmentedInputAndReplies(t *testing.T) {
	var p inputParser
	var keys, replies []string
	key := func(s string) { keys = append(keys, s) }
	reply := func(s string) { replies = append(replies, s) }
	now := time.Now()
	input := "12G\x1b[A\x1bPregion secondary status 2 5 300000 100 0 0\x1b\\:open café.pdf\r\x1b[<64;3;4M\x1b[6~"
	for _, b := range []byte(input) {
		p.feed([]byte{b}, now, key, reply)
	}
	want := []string{"1", "2", "G", "up", ":", "o", "p", "e", "n", " ", "c", "a", "f", "é", ".", "p", "d", "f", "\r", "mouse:<64;3;4M", "pagedown"}
	if !reflect.DeepEqual(keys, want) || len(replies) != 1 || len(p.pending) != 0 {
		t.Fatalf("keys=%q replies=%q pending=%q", keys, replies, p.pending)
	}
	p.feed([]byte("\x1b"), now, key, reply)
	p.expire(now.Add(time.Second), key)
	if keys[len(keys)-1] != "escape" {
		t.Fatal("standalone Escape was not resolved")
	}
	p.feed([]byte("\x1bP"+strings.Repeat("x", 9000)), now, key, reply)
	if len(p.pending) != 0 {
		t.Fatal("unbounded protocol input")
	}
}

func TestViewerBindingsAndCommands(t *testing.T) {
	var out bytes.Buffer
	v := &viewer{options: options{file: "a.pdf", zoom: 100, background: "#f7f5ef"}, out: &out, marks: make(map[string]int), pages: 12, position: 350000}
	var p inputParser
	p.feed([]byte("3j\x042gg7G50%z0ziJmaG'a'/needle\r"), time.Now(), v.key, v.reply)
	for _, command := range []string{"scroll 3", "view 500000", "page 2", "page 7", "position 500000", "zoom 100", "zoom 110", "pages 1", "position 1000000", "position 350000"} {
		if !strings.Contains(out.String(), "\x1bPregion secondary "+command+"\x1b\\") {
			t.Errorf("missing %q: %q", command, out.String())
		}
	}
	out.Reset()
	v.prefix, v.prompt = "", ""
	p.feed([]byte("/needle\r"), time.Now(), v.key, v.reply)
	v.key("n")
	v.key("N")
	for _, direction := range []string{"forward", "next", "previous"} {
		if !strings.Contains(out.String(), "find "+direction+" 'needle'") {
			t.Errorf("missing search %s", direction)
		}
	}
	v.reply("region secondary status 4 12 400000 80 0 1")
	if v.page != 4 || v.zoom != 80 || !v.failed {
		t.Fatalf("incorrect status: %+v", v)
	}
	v.execute("page nope")
	if !strings.Contains(v.message, "Usage") {
		t.Fatal("invalid command was accepted")
	}
	v.key(":")
	v.key("q")
	v.key("escape")
	if v.quit || v.prompt != "" {
		t.Fatal("Escape did not cancel the command")
	}
	v.execute("q")
	if !v.quit {
		t.Fatal("quit command failed")
	}
}

func TestOptionsAndPathProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a;'quoted'.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	o, err := parseOptions([]string{"-page", "3", "-zoom", "120", path}, io.Discard)
	if err != nil || o.page != 3 || o.zoom != 120 {
		t.Fatalf("options=%+v err=%v", o, err)
	}
	var out bytes.Buffer
	v := &viewer{options: o, out: &out}
	v.open(path)
	if strings.Count(out.String(), "\x1bP") != 1 || !strings.Contains(out.String(), "'\"'\"'") {
		t.Fatalf("unsafe path quoting: %q", out.String())
	}
	for _, args := range [][]string{{}, {"-zoom", "501", path}, {"-page", "0", path}, {"-bg", "oops", path}, {t.TempDir()}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	if got := statusText("a\x1b界\n", 3); got != "a??" {
		t.Fatalf("unsafe chrome: %q", got)
	}
}
