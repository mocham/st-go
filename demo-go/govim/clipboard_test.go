package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testClipboard struct {
	text             string
	primary          bool
	copies, requests int
}

func TestPTYTerminalClipboardAndImmediateInternalPaste(t *testing.T) {
	path := filepath.Join(t.TempDir(), "document.txt")
	os.WriteFile(path, []byte("one\n"), 0600)
	p := startPTYEditor(t, path)
	p.send(t, "yyp")
	copyFrame := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("one\n")) + "\x1b\\"
	p.await(t, "terminal clipboard write", func() bool { return strings.Contains(p.text(), copyFrame) })
	p.send(t, "\"+p")
	p.await(t, "terminal clipboard query", func() bool { return strings.Contains(p.text(), "\x1b]52;c;?\x1b\\") })
	p.send(t, "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte("HOST"))+"\x1b\\")
	p.await(t, "clipboard paste", func() bool { return strings.Contains(p.text(), "HOST") })
	p.send(t, "p:wq\r")
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("clipboard PTY did not exit")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "one\noHOSTne\none\n" {
		t.Fatalf("clipboard/internal register separation: %q %v", data, err)
	}
}

func (c *testClipboard) Set(text string, primary bool) error {
	c.text, c.primary = text, primary
	c.copies++
	return nil
}
func (c *testClipboard) Request(primary bool) error { c.requests++; c.primary = primary; return nil }
func (c *testClipboard) Close() error               { return nil }

func TestClipboardMappings(t *testing.T) {
	for _, keys := range []string{"2yy", "2dd", "Vjy", "Vjd", ":1,2Y<Enter>", ":1,2D<Enter>"} {
		t.Run(keys, func(t *testing.T) {
			e := memoryEditor("one\ntwo\nthree\n")
			c := &testClipboard{}
			e.clip = c
			editKeys(e, keys)
			if c.copies != 1 || c.text != "one\ntwo\n" || c.primary || c.requests != 0 {
				t.Fatalf("mapping copied %+v", c)
			}
		})
	}
}
func TestInternalRegistersNeverReadHostClipboard(t *testing.T) {
	e := memoryEditor("one\ntwo\n")
	c := &testClipboard{}
	e.clip = c
	editKeys(e, "yy")
	c.text = "unrelated host clipboard"
	editKeys(e, "p")
	if editorText(e) != "one\none\ntwo\n" || c.requests != 0 || e.clipboardRead != nil {
		t.Fatal("internal paste depended on host clipboard")
	}
}
func TestClipboardReadAsynchronousAndSeparate(t *testing.T) {
	e := memoryEditor("abc\n")
	c := &testClipboard{}
	e.clip = c
	e.registers['"'] = register{Text: "internal"}
	editKeys(e, "\"+p")
	if c.requests != 1 || e.clipboardRead == nil || editorText(e) != "abc\n" {
		t.Fatal("host read was not deferred")
	}
	e.Feed(keyEvent{Clipboard: &clipboardReply{selection: 'p', text: "wrong target"}})
	if editorText(e) != "abc\n" {
		t.Fatal("mismatched clipboard reply accepted")
	}
	e.Feed(keyEvent{Clipboard: &clipboardReply{selection: 'c', text: "世界"}})
	if editorText(e) != "a世界bc\n" || e.registers['"'].Text != "internal" {
		t.Fatal("host reply changed internal register or was not pasted")
	}
	e = memoryEditor("abc\n")
	e.clip = c
	editKeys(e, "\"+p")
	e.tick(time.Now().Add(clipboardTimeout + time.Second))
	if e.clipboardRead != nil || editorText(e) != "abc\n" {
		t.Fatal("timeout mutated document")
	}
}
func TestClipboardProtocolRoundTrip(t *testing.T) {
	var out bytes.Buffer
	c := &terminalClipboard{out: &out}
	text := "café 世界\n\x1b:q!"
	if err := c.Set(text, false); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("copy performed terminal I/O before flush")
	}
	c.Request(true)
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x1b\\\x1b]52;p;?\x1b\\"
	if out.String() != want {
		t.Fatalf("OSC 52=%q", out.String())
	}
	for chunk := 1; chunk < 10; chunk++ {
		var parser inputParser
		var replies []*clipboardReply
		wire := []byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x1b\\")
		for at := 0; at < len(wire); at += chunk {
			parser.feed(wire[at:min(len(wire), at+chunk)], time.Now(), func(event keyEvent) {
				if event.Clipboard == nil {
					t.Fatal("clipboard data became commands")
				}
				replies = append(replies, event.Clipboard)
			})
		}
		if len(replies) != 1 || replies[0].err != nil || replies[0].text != text {
			t.Fatalf("chunk%d reply=%+v", chunk, replies)
		}
	}
	if err := c.Set(strings.Repeat("x", maxClipboardBytes+1), false); err == nil {
		t.Fatal("oversized clipboard queued")
	}
}
