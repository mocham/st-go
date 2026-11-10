package frontend

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
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
	img := solidImage{image.NewUniform(color.Black), image.Rect(0, 0, 8193, 8192)}
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

func TestLargeJPEGPreviewWithinDecodedImageLimit(t *testing.T) {
	trm := regionTestTerminal(t)
	var encoded bytes.Buffer
	// Just over the old 64 MiB RGBA limit; the compressed input and preview
	// remain small. Use a uniform source rather than allocating source pixels.
	img := solidImage{image.NewUniform(color.RGBA{R: 200, G: 80, B: 30, A: 255}), image.Rect(0, 0, 4200, 4000)}
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	cols, rows, glyphs, ok := trm.ImageDecode(encoded.Bytes(), term.ImageDecodeOptions{FitContain: true})
	if !ok || cols < 1 || rows < 1 || cols > trm.cols || rows > trm.rows || len(glyphs) != cols*rows {
		t.Fatalf("large JPEG preview failed: %dx%d, %d glyphs, ok=%v", cols, rows, len(glyphs), ok)
	}
	for _, glyph := range glyphs {
		offset := int(glyph.Fg)
		if glyph.U == term.ImageRune && offset < len(trm.imageAtlas) && trm.imageAtlas[offset]&0xffffff != 0 {
			return
		}
	}
	t.Fatal("accepted large JPEG produced an all-black preview")
}

func TestFramebufferResourceLimit(t *testing.T) {
	trm := &Terminal{}
	trm.ensureFramebuffer(8, 8)
	trm.framebuf[0] = 123
	if trm.ensureFramebuffer(1<<30, 1<<30) {
		t.Fatal("accepted oversized framebuffer")
	}
	if trm.fbW != 8 || trm.fbH != 8 || trm.framebuf[0] != 123 {
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
	prop := native(trm).Atom("CLIPBOARD")
	begin := func(size uint32) {
		xproto.ChangeProperty(native(trm).Connection(), xproto.PropModeReplace, native(trm).WindowID(), prop,
			native(trm).Atom("INCR"), 32, 1, uint32Bytes(size))
		trm.selnotify(xproto.SelectionNotifyEvent{Requestor: native(trm).WindowID(), Property: prop})
	}
	chunk := func(data []byte) {
		xproto.ChangeProperty(native(trm).Connection(), xproto.PropModeReplace, native(trm).WindowID(), prop,
			native(trm).Atom("UTF8_STRING"), 8, uint32(len(data)), data)
		trm.propnotify(xproto.PropertyNotifyEvent{Window: native(trm).WindowID(), Atom: prop, State: xproto.PropertyNewValue})
	}
	begin(0) // INCR size is only a hint; enforce the actual cumulative size.
	block := bytes.Repeat([]byte("x"), 65536)
	for n := 0; n < limits.ClipboardBytes; n += len(block) {
		chunk(block)
	}
	chunk([]byte("overflow"))
	if !native(trm).PasteState().Discard || len(native(trm).PasteState().Data) != 0 {
		t.Fatal("oversized transfer was retained")
	}
	chunk(nil)
	if native(trm).PasteState().Active || pasted.Len() != 0 {
		t.Fatal("oversized transfer pasted a partial payload")
	}
	begin(limits.ClipboardBytes + 1)
	if !native(trm).PasteState().Discard {
		t.Fatal("oversized announced transfer was accepted")
	}
	chunk(nil)
	begin(5)
	trm.propnotify(xproto.PropertyNotifyEvent{Window: native(trm).WindowID(), Atom: native(trm).Atom("WM_NAME"), State: xproto.PropertyNewValue})
	if !native(trm).PasteState().Active || len(native(trm).PasteState().Data) != 0 {
		t.Fatal("unrelated property changed the transfer")
	}
	chunk([]byte("hello"))
	chunk(nil)
	if pasted.String() != "hello" || native(trm).PasteState().Active {
		t.Fatalf("valid transfer after rejection = %q, active=%v", pasted.String(), native(trm).PasteState().Active)
	}
}
