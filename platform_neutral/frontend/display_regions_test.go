package frontend

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/xgb/xproto"
	"st-go/config"
	"st-go/term"
)

func regionTestTerminal(t *testing.T) *Terminal {
	t.Helper()
	cfg := config.Default()
	cfg.Cols, cfg.Rows = 20, 5
	trm := &Terminal{cfg: cfg, cols: 20, rows: 5, cw: 4, ch: 8, baseline: 6, borderpx: 1, done: make(chan struct{})}
	trm.loadColors(cfg)
	ensureFramebuffer(trm.cols*trm.cw+2, trm.rows*trm.ch+2)
	trm.attachTerm(term.NewTerm(cfg, trm))
	trm.termCore.Redraw()
	t.Cleanup(func() {
		trm.mu.Lock()
		trm.Close()
		trm.mu.Unlock()
	})
	return trm
}

func splitRegionTest(t *testing.T, trm *Terminal) {
	t.Helper()
	trm.writeTerminalOutput(trm.termCore, []byte("\x1bPregion split 40%\x1b\\"))
	if trm.cols != 12 || trm.termCore.Cols() != 12 {
		t.Fatalf("main width = %d, core width = %d", trm.cols, trm.termCore.Cols())
	}
}

func setRegionTestPixels(trm *Terminal, pixel uint32) {
	r := trm.secondaryPixels()
	bitmap := regionBitmap{width: r.Dx(), height: r.Dy(), pixels: make([]uint32, r.Dx()*r.Dy())}
	for i := range bitmap.pixels {
		bitmap.pixels[i] = pixel
	}
	trm.displayRegions.mu.Lock()
	trm.displayRegions.secondary.bitmap = bitmap
	trm.displayRegions.secondary.dirty = true
	trm.requestRegionPaint()
	trm.displayRegions.mu.Unlock()
	trm.paint()
}

func assertRegionPixels(t *testing.T, trm *Terminal, pixel uint32) {
	t.Helper()
	trm.paint()
	r := trm.secondaryPixels()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if got := framebuf[y*fbW+x]; got != pixel {
				t.Fatalf("preview pixel (%d,%d) = %#x, want %#x", x, y, got, pixel)
			}
		}
	}
}

func TestDisplayRegionSurvivesTerminalOperations(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	const pixel = 0xff913b57
	setRegionTestPixels(trm, pixel)
	for _, output := range []string{
		"\x1b[2J\x1b[Hhello\x1b[K", // Vim startup / Ctrl-L erase operations
		strings.Repeat("scroll this line\r\n", 30),
		"\x1b[?1049h\x1b[2Jalternate\x1b[?1049l",
		"\x1bc", // reset, including the terminal image atlas
		"\x1bPclear\x1b\\",
	} {
		trm.writeTerminalOutput(trm.termCore, []byte(output))
		trm.paintTerminalOutput(trm.termCore)
		assertRegionPixels(t, trm, pixel)
	}
	// Exercise the final clipping layer even with deliberately out-of-grid
	// draws, including wide cells and source-stride preservation.
	trm.drawGlyphAt(0, 0, 0xff00ff00, trm.cols-1, 0, true)
	trm.fillRect(0xff00ff00, -100, -100, fbW+200, fbH+200)
	imageAtlas = make([]uint32, trm.cw*trm.ch)
	trm.drawImageCell(term.Glyph{U: term.ImageRune}, trm.cols, 0)
	assertRegionPixels(t, trm, pixel)
	trm.handleEvent(xproto.ExposeEvent{})
	assertRegionPixels(t, trm, pixel)
}

func TestDisplayRegionLayoutResizeAndReset(t *testing.T) {
	trm := regionTestTerminal(t)
	var sizes [][2]int
	trm.ttyResize = func(rows, cols int) { sizes = append(sizes, [2]int{rows, cols}) }
	splitRegionTest(t, trm)
	setRegionTestPixels(trm, 0xff913b57)
	before := trm.Regions()
	for _, invalid := range [][]term.DisplayRegion{
		{{ID: term.MainRegion, Bounds: term.CellRect{Width: 20, Height: 2}}, {ID: term.SecondaryRegion, Bounds: term.CellRect{Y: 1, Width: 20, Height: 4}}},
		{{ID: term.MainRegion, Bounds: term.CellRect{Width: 10, Height: 5}}, {ID: term.SecondaryRegion, Bounds: term.CellRect{X: 9, Width: 11, Height: 5}}},
		{{ID: term.MainRegion, Bounds: term.CellRect{Width: 10, Height: 5}}, {ID: term.SecondaryRegion, Bounds: term.CellRect{X: 11, Width: 9, Height: 5}}},
		{{ID: "other", Bounds: term.CellRect{Width: 20, Height: 5}}},
		{{ID: term.MainRegion, Bounds: term.CellRect{Width: int(^uint(0) >> 1), Height: 5}}},
	} {
		if err := trm.SetRegions(invalid); err == nil || !reflect.DeepEqual(before, trm.Regions()) {
			t.Fatalf("invalid layout changed display: %#v, %v", invalid, err)
		}
	}
	trm.resize(40*trm.cw+2, 7*trm.ch+2)
	if trm.cols != 24 || trm.rows != 7 || trm.Regions()[1].Bounds.Width != 16 {
		t.Fatalf("resized regions = %#v", trm.Regions())
	}
	assertRegionPixels(t, trm, 0xff913b57)
	trm.resize(trm.cw+2, 7*trm.ch+2)
	if trm.cols != 1 || trm.Regions()[1].Bounds.Width != 0 {
		t.Fatal("one-column window did not suspend preview")
	}
	trm.resize(20*trm.cw+2, 5*trm.ch+2)
	assertRegionPixels(t, trm, 0xff913b57)
	if err := trm.SetRegions(nil); err != nil {
		t.Fatal(err)
	}
	if trm.cols != 20 || trm.termCore.Cols() != 20 || len(trm.Regions()) != 1 || sizes[len(sizes)-1] != [2]int{5, 20} {
		t.Fatalf("reset did not restore PTY/core: regions=%#v, sizes=%v", trm.Regions(), sizes)
	}
}

func TestStackedRegionClippingResizeAndMouse(t *testing.T) {
	trm := regionTestTerminal(t)
	var sizes [][2]int
	trm.ttyResize = func(rows, cols int) { sizes = append(sizes, [2]int{rows, cols}) }
	trm.writeTerminalOutput(trm.termCore, []byte("\x1bPregion stack 1\x1b\\"))
	if trm.rows != 1 || trm.cols != 20 || trm.Regions()[1].Bounds != (term.CellRect{Y: 1, Width: 20, Height: 4}) {
		t.Fatalf("stack: %#v", trm.Regions())
	}
	setRegionTestPixels(trm, 0xff913b57)
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[2J\x1b[Hstatus\r\nmore\x1b[K"))
	trm.fillRect(0xff123456, -100, -100, fbW+200, fbH+200)
	assertRegionPixels(t, trm, 0xff913b57)
	trm.resize(30*trm.cw+2, 8*trm.ch+2)
	if trm.rows != 1 || trm.cols != 30 || trm.Regions()[1].Bounds.Height != 7 {
		t.Fatalf("resized stack: %#v", trm.Regions())
	}
	assertRegionPixels(t, trm, 0xff913b57)
	buttons = 0
	t.Cleanup(func() { buttons = 0 })
	var input bytes.Buffer
	trm.termCore.SetWriter(func(b []byte) { input.Write(b) })
	trm.termCore.Twrite([]byte("\x1b[?1000h\x1b[?1006h"), false)
	trm.bpress(xproto.ButtonPressEvent{Detail: 1, EventX: 2, EventY: 12})
	trm.brelease(xproto.ButtonReleaseEvent{Detail: 1, EventX: 2, EventY: 2})
	if input.Len() != 0 || trm.regionButtons != 0 {
		t.Fatal("PDF click leaked into command row")
	}
	trm.bpress(xproto.ButtonPressEvent{Detail: 1, EventX: 2, EventY: 2})
	trm.brelease(xproto.ButtonReleaseEvent{Detail: 1, EventX: 2, EventY: 12})
	if !strings.Contains(input.String(), "m") {
		t.Fatal("command row lost mouse release")
	}
	trm.resize(30*trm.cw+2, trm.ch+2)
	if trm.Regions()[1].Bounds.Height != 0 {
		t.Fatal("one-row window did not suspend PDF")
	}
	trm.resize(30*trm.cw+2, 8*trm.ch+2)
	assertRegionPixels(t, trm, 0xff913b57)
	if err := trm.SetRegions(nil); err != nil {
		t.Fatal(err)
	}
	if trm.rows != 8 || trm.cols != 30 || sizes[len(sizes)-1] != [2]int{8, 30} {
		t.Fatalf("reset: %#v %v", trm.Regions(), sizes)
	}
}

func TestDisplayRegionSynchronizedOutput(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	const oldPixel, newPixel = 0xff913b57, 0xff456789
	setRegionTestPixels(trm, oldPixel)
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[?2026h"))
	setRegionTestPixels(trm, newPixel)
	assertRegionPixels(t, trm, oldPixel)
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[?2026l"))
	assertRegionPixels(t, trm, newPixel)
	trm.writeTerminalOutput(trm.termCore, []byte("\x1bPregion secondary clear\x1b\\"))
	assertRegionPixels(t, trm, trm.colorAt(ColDefaultBg))
}

func TestDisplayRegionMouseOwnership(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	buttons, msel = 0, mouseSel{}
	t.Cleanup(func() { buttons, msel = 0, mouseSel{} })
	var input bytes.Buffer
	trm.termCore.SetWriter(func(b []byte) { input.Write(b) })
	trm.termCore.Twrite([]byte("\x1b[?1000h\x1b[?1006h"), false)
	right := int16(trm.borderpx + trm.cols*trm.cw + 1)
	trm.bpress(xproto.ButtonPressEvent{Detail: 1, EventX: right, EventY: 2})
	trm.bmotion(xproto.MotionNotifyEvent{EventX: 2, EventY: 2})
	trm.brelease(xproto.ButtonReleaseEvent{Detail: 1, EventX: 2, EventY: 2})
	if input.Len() != 0 || buttons != 0 || trm.regionButtons != 0 {
		t.Fatal("secondary click leaked into Vim input")
	}
	trm.bpress(xproto.ButtonPressEvent{Detail: 1, EventX: 2, EventY: 2})
	trm.brelease(xproto.ButtonReleaseEvent{Detail: 1, EventX: right, EventY: 2})
	if !strings.Contains(input.String(), "M") || !strings.Contains(input.String(), "m") || buttons != 0 {
		t.Fatalf("main drag lost its matching release: %q", input.String())
	}
}

func TestDisplayRegionAsyncImage(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	path := filepath.Join(t.TempDir(), "preview.png")
	var encoded bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.SetRGBA(x, y, color.RGBA{0x91, 0x3b, 0x57, 0xff})
		}
	}
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	trm.mu.Lock()
	if err := trm.OpenRegion(term.SecondaryRegion, term.RegionImage{Path: path, Options: term.ImageDecodeOptions{FitHeight: true}}); err != nil {
		trm.mu.Unlock()
		t.Fatal(err)
	}
	trm.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ready := func() bool {
			trm.mu.Lock()
			defer trm.mu.Unlock()
			trm.displayRegions.mu.Lock()
			ready := len(trm.displayRegions.secondary.bitmap.pixels) > 0
			trm.displayRegions.mu.Unlock()
			if !ready {
				return false
			}
			assertRegionPixels(t, trm, 0xff913b57)
			return true
		}()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("preview worker did not publish the bitmap")
		}
		time.Sleep(time.Millisecond)
	}
	// Queue work then clear/reset while holding the serialization lock. Any
	// in-flight result must not resurrect the cleared/closed preview.
	trm.mu.Lock()
	_ = trm.OpenRegion(term.SecondaryRegion, term.RegionImage{Path: path})
	_ = trm.ClearRegion(term.SecondaryRegion)
	_ = trm.SetRegions(nil)
	trm.mu.Unlock()
}

func TestDisplayRegionRejectsStaleResults(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	setRegionTestPixels(trm, 0xff913b57)
	job := regionJob{generation: trm.displayRegions.generation}
	bitmap := trm.displayRegions.secondary.bitmap
	_ = trm.ClearRegion(term.SecondaryRegion)
	trm.acceptRegionImage(job, bitmap, nil)
	assertRegionPixels(t, trm, trm.colorAt(ColDefaultBg))
	if len(trm.displayRegions.secondary.bitmap.pixels) != 0 {
		t.Fatal("in-flight decode resurrected a cleared preview")
	}
	job.generation = trm.displayRegions.generation
	_ = trm.SetRegions(nil)
	splitRegionTest(t, trm)
	trm.acceptRegionImage(job, bitmap, nil)
	assertRegionPixels(t, trm, trm.colorAt(ColDefaultBg))
}

func TestDisplayRegionRefreshKeepsPage(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	// Capture requests without running native decoders in this state test.
	trm.displayRegions.jobs = make(chan regionJob, 1)
	if err := trm.OpenRegion(term.SecondaryRegion, term.RegionImage{Path: "old.pdf", Options: term.ImageDecodeOptions{Page: 4}}); err != nil {
		t.Fatal(err)
	}
	job := <-trm.displayRegions.jobs
	trm.acceptRegionImage(job, regionBitmap{page: 1}, nil) // wrapped against page count
	if err := trm.OpenRegion(term.SecondaryRegion, term.RegionImage{Path: "new.pdf", KeepPage: true}); err != nil {
		t.Fatal(err)
	}
	job = <-trm.displayRegions.jobs
	if job.request.Options.Page != 1 || job.request.Path != "new.pdf" {
		t.Fatalf("refresh lost visible page: %#v", job.request)
	}
}

func TestDisplayRegionNavigationReporting(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	buttons = 0
	t.Cleanup(func() { buttons = 0 })
	var input bytes.Buffer
	trm.termCore.SetWriter(func(b []byte) { input.Write(b) })
	trm.writeTerminalOutput(trm.termCore, []byte("\x1bPregion secondary scroll report; region secondary seek 42\x1b\\"))
	right := int16(trm.borderpx + trm.cols*trm.cw + 1)
	trm.bpress(xproto.ButtonPressEvent{Detail: 4, EventX: right, EventY: 2})
	trm.brelease(xproto.ButtonReleaseEvent{Detail: 4, EventX: right, EventY: 2})
	want := "\x1bPregion secondary event seek 42\x1b\\\x1bPregion secondary event scroll -1\x1b\\"
	if input.String() != want {
		t.Fatalf("navigation events = %q", input.String())
	}
	input.Reset()
	trm.writeTerminalOutput(trm.termCore, []byte("\x1bPregion secondary scroll pages\x1b\\"))
	trm.bpress(xproto.ButtonPressEvent{Detail: 5, EventX: right, EventY: 2})
	trm.brelease(xproto.ButtonReleaseEvent{Detail: 5, EventX: right, EventY: 2})
	if input.Len() != 0 {
		t.Fatal("page mode leaked navigation events to the application")
	}
}

func TestPDFRegionFitWidthClipsVertically(t *testing.T) {
	// Tall page: top quarter red, the rest green. Contain shows both; fit-width
	// fills the whole 100x100 viewport with the red top without a huge bitmap.
	stream := "0 1 0 rg 0 0 100 400 re f 1 0 0 rg 0 300 100 100 re f\n"
	objects := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 100 400]/Contents 4 0 R>>",
		fmt.Sprintf("<</Length %d>>\nstream\n%sendstream", len(stream), stream),
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	var offsets []int
	for i, object := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 5\n0000000000 65535 f \n")
	for _, offset := range offsets {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer<</Size 5/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", xref)
	contain, _, ok := renderPDFPageViewport(pdf.Bytes(), 0, 100, 100, false)
	if !ok {
		t.Fatal("contain decode failed")
	}
	width, _, ok := renderPDFPageViewport(pdf.Bytes(), 0, 100, 100, true)
	if !ok || len(width) != 100*100*4 {
		t.Fatal("fit-width decode failed")
	}
	center := (50*100 + 50) * 4
	left := (50*100 + 5) * 4
	if contain[center+1] < 200 || width[center+2] < 200 || width[left+2] < 200 || width[center+1] > 50 {
		t.Fatal("fit-width did not fill the width and crop the tall page")
	}
}
