package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/BurntSushi/xgb/xproto"
	"st-go/config"
	"st-go/limits"
	"st-go/term"
)

type solidImage struct {
	*image.Uniform
	rect image.Rectangle
}

func (i solidImage) Bounds() image.Rectangle { return i.rect }

func oversizedPNG(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	// A valid, tiny compressed file whose decoded pixels exceed the budget.
	img := solidImage{image.NewUniform(color.Black), image.Rect(0, 0, 4097, 4096)}
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestImageDecodeResourceLimit(t *testing.T) {
	if _, _, _, failed := decodeImage(oversizedPNG(t)); !failed {
		t.Fatal("decoded an oversized PNG")
	}
	if _, _, _, failed := decodeImage(makeTestPNG(t)); failed {
		t.Fatal("small image failed after rejection")
	}
}

func TestFramebufferResourceLimit(t *testing.T) {
	ensureFramebuffer(8, 8)
	framebuf[0] = 123
	if ensureFramebuffer(1<<30, 1<<30) {
		t.Fatal("accepted oversized framebuffer")
	}
	if fbW != 8 || fbH != 8 || framebuf[0] != 123 {
		t.Fatal("rejected resize replaced the framebuffer")
	}
}

func TestPDFBitmapResourceLimit(t *testing.T) {
	if pixels, _, ok := renderPDFPage([]byte("%PDF-1.4"), 0, 1<<30, 1<<30); ok || pixels != nil {
		t.Fatal("accepted oversized PDF output")
	}
}

func TestWebPAnimationCanvasLimit(t *testing.T) {
	data := makeAnimWebp(t)
	if data == nil {
		t.Skip("ffmpeg not available")
	}
	i := bytes.Index(data, []byte("VP8X"))
	if i < 0 || i+18 > len(data) {
		t.Fatal("missing animation canvas header")
	}
	// Enlarge the canvas while retaining its small, valid animation frames.
	for _, start := range []int{i + 12, i + 15} {
		data[start], data[start+1], data[start+2] = 0xff, 0x1f, 0 // 8192
	}
	if decoder, ok := openWebPAnimDecoder(data); ok {
		decoder.close()
		t.Fatal("allocated oversized animation canvas")
	}
	if _, _, _, ok := decodeWebPAnimInfo(data); ok {
		t.Fatal("accepted oversized animation metadata")
	}
}

func TestClipboardResourceLimits(t *testing.T) {
	cfg := config.Default()
	cfg.WebPCachePath = ""
	trm, err := NewTerminal(cfg)
	if err != nil {
		t.Skipf("no X server: %v", err)
	}
	defer trm.Close()
	trm.termCore = term.NewTerm(cfg, trm)
	var pasted bytes.Buffer
	trm.termCore.SetWriter(func(b []byte) { pasted.Write(b) })
	prop := trm.atoms["CLIPBOARD"]
	begin := func(size uint32) {
		xproto.ChangeProperty(trm.conn, xproto.PropModeReplace, trm.win, prop,
			trm.atoms["INCR"], 32, 1, uint32Bytes(size))
		trm.selnotify(xproto.SelectionNotifyEvent{Requestor: trm.win, Property: prop})
	}
	chunk := func(data []byte) {
		xproto.ChangeProperty(trm.conn, xproto.PropModeReplace, trm.win, prop,
			trm.atoms["UTF8_STRING"], 8, uint32(len(data)), data)
		trm.propnotify(xproto.PropertyNotifyEvent{Window: trm.win, Atom: prop, State: xproto.PropertyNewValue})
	}
	begin(0) // INCR size is only a hint; enforce the actual cumulative size.
	block := bytes.Repeat([]byte("x"), 65536)
	for n := 0; n < limits.ClipboardBytes; n += len(block) {
		chunk(block)
	}
	chunk([]byte("overflow"))
	if !trm.incrDiscard || len(trm.incrData) != 0 {
		t.Fatal("oversized transfer was retained")
	}
	chunk(nil)
	if trm.incrActive || pasted.Len() != 0 {
		t.Fatal("oversized transfer pasted a partial payload")
	}
	begin(limits.ClipboardBytes + 1)
	if !trm.incrDiscard {
		t.Fatal("oversized announced transfer was accepted")
	}
	chunk(nil)
	begin(5)
	trm.propnotify(xproto.PropertyNotifyEvent{Window: trm.win, Atom: trm.atoms["WM_NAME"], State: xproto.PropertyNewValue})
	if !trm.incrActive || len(trm.incrData) != 0 {
		t.Fatal("unrelated property changed the transfer")
	}
	chunk([]byte("hello"))
	chunk(nil)
	if pasted.String() != "hello" || trm.incrActive {
		t.Fatalf("valid transfer after rejection = %q, active=%v", pasted.String(), trm.incrActive)
	}
}
