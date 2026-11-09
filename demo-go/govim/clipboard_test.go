package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"github.com/BurntSushi/xgb/xproto"
)

type testClipboard struct {
	text    string
	primary bool
	copies  int
}

func (c *testClipboard) Set(text string, primary bool) error {
	c.text, c.primary = text, primary
	c.copies++
	return nil
}
func (c *testClipboard) Get(bool) (string, error) { return c.text, nil }
func (c *testClipboard) Close() error             { return nil }

func TestClipboardMappings(t *testing.T) {
	for _, keys := range []string{"2yy", "2dd", "Vjy", "Vjd", ":1,2Y<Enter>", ":1,2D<Enter>"} {
		t.Run(keys, func(t *testing.T) {
			e := memoryEditor("one\ntwo\nthree\n")
			c := &testClipboard{}
			e.clip = c
			editKeys(e, keys)
			if c.copies != 1 || c.text != "one\ntwo\n" || c.primary {
				t.Fatalf("mapping %s copied %+v", keys, c)
			}
		})
	}
	e := memoryEditor("abc\n")
	c := &testClipboard{text: "世界"}
	e.clip = c
	editKeys(e, "\"+p")
	if editorText(e) != "a世界bc\n" {
		t.Fatal("native clipboard register paste")
	}
}

func TestClipboardWireBoundsAndEncoding(t *testing.T) {
	text, primary := strings.Repeat("\x00\x1b<&世界", 1000), "café"
	var data bytes.Buffer
	var header [8]byte
	binary.LittleEndian.PutUint32(header[:4], uint32(len(text)))
	binary.LittleEndian.PutUint32(header[4:], uint32(len(primary)))
	data.Write(header[:])
	data.WriteString(text)
	data.WriteString(primary)
	a, z, err := readClipboardPayload(&data)
	if err != nil || a != text || z != primary {
		t.Fatal("clipboard handoff changed binary/control text")
	}
	binary.LittleEndian.PutUint32(header[:4], ^uint32(0))
	if _, _, err := readClipboardPayload(bytes.NewReader(header[:])); err == nil {
		t.Fatal("unbounded clipboard allocation")
	}
	c := &xClipboard{atoms: map[string]xproto.Atom{"UTF8_STRING": 1, "STRING": 2}}
	if got, err := c.decodeText(2, 8, []byte{'c', 'a', 'f', 0xe9}); err != nil || got != "café" {
		t.Fatal("STRING was not decoded as Latin-1")
	}
	if _, err := c.decodeText(1, 8, []byte{0xff}); err == nil {
		t.Fatal("invalid UTF-8 clipboard accepted")
	}
	if latin1Text("café 世界") != "caf\xe9 ??" {
		t.Fatal("Latin-1 conversion")
	}
}

// Use an existing isolated X server. These tests do not install, download, or
// launch any X server/font packages, and never run an external clipboard tool.
func TestNativeClipboardX11(t *testing.T) {
	if os.Getenv("GOVIM_TEST_X11") != "1" {
		t.Skip("set GOVIM_TEST_X11=1 with DISPLAY pointing at an existing isolated X server")
	}
	owner, err := newXClipboard(false)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	reader, err := newXClipboard(true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { reader.mu.Lock(); reader.keep = false; reader.mu.Unlock(); reader.Close() }()
	for _, text := range []string{"café 世界 α", strings.Repeat("large 世界\n", 20000), ""} {
		if err := owner.Set(text, false); err != nil {
			t.Fatal(err)
		}
		got, err := reader.Get(false)
		if err != nil || got != text {
			t.Fatalf("X11 transfer: bytes=%d want=%d err=%v", len(got), len(text), err)
		}
	}
	if err := owner.Set("primary selection", true); err != nil {
		t.Fatal(err)
	}
	if got, err := reader.Get(true); err != nil || got != "primary selection" {
		t.Fatalf("PRIMARY transfer: %q %v", got, err)
	}
	// The clipboard remains readable after the editor's original owner closes.
	owner.mu.Lock()
	owner.keep = true
	owner.mu.Unlock()
	if err := owner.Set("persisted 世界", false); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := reader.Get(false); err != nil || got != "persisted 世界" {
		t.Fatalf("clipboard handoff: %q %v", got, err)
	}
	reader.Set("release helper", false)
	reader.Set("release primary", true)
}
