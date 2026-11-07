package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/xgb/xproto"
	"st-go/limits"
	"st-go/term"
)

func continuousTestPDF(cropBox string) []byte {
	streams := []string{
		"0 1 0 rg 0 0 100 400 re f 1 0 0 rg 0 300 100 100 re f\n",
		"0 0 1 rg 0 0 100 200 re f\n",
	}
	objects := []string{"<</Type/Catalog/Pages 2 0 R>>", "<</Type/Pages/Kids[3 0 R 5 0 R]/Count 2>>"}
	for i, height := range []int{400, 200} {
		box := ""
		if i == 0 {
			box = cropBox
		}
		objects = append(objects, fmt.Sprintf("<</Type/Page/Parent 2 0 R/MediaBox[0 0 100 %d]%s/Contents %d 0 R>>", height, box, 4+2*i),
			fmt.Sprintf("<</Length %d>>\nstream\n%sendstream", len(streams[i]), streams[i]))
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	var offsets []int
	for i, object := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return pdf.Bytes()
}

func TestContinuousPDFStripAcrossPages(t *testing.T) {
	pdf, err := openScrollPDF(continuousTestPDF(""))
	if err != nil {
		t.Fatal(err)
	}
	defer pdf.close()
	for _, tc := range []struct {
		position, y int
		color       uint32
	}{
		{0, 50, 0xffff0000}, {0, 150, 0xff00ff00},
		{360, 350, 0xff00ff00}, {360, 450, 0xff0000ff}, {999, 590, 0xff0000ff},
	} {
		bitmap, err := pdf.render(100, 80, tc.position)
		if err != nil {
			t.Fatal(err)
		}
		if bitmap.contentHeight != 600 || bitmap.height != 240 || len(bitmap.pixels)*4 > limits.PreviewCacheBytes {
			t.Fatalf("unbounded or incorrect strip geometry: %#v", bitmap)
		}
		if got := bitmap.pixels[(tc.y-bitmap.top)*bitmap.width+50]; got != tc.color {
			t.Fatalf("document pixel at y=%d: %#x, want %#x (strip top %d)", tc.y, got, tc.color, bitmap.top)
		}
	}
	resized, err := pdf.render(200, 80, 820)
	if err != nil || resized.contentHeight != 1200 {
		t.Fatalf("fit-width resize failed: height=%d, err=%v", resized.contentHeight, err)
	}
}

func TestContinuousPDFCropBoxKeepsContentAndJoinsPages(t *testing.T) {
	pdf, err := openScrollPDF(continuousTestPDF("/CropBox[0 250 100 400]"))
	if err != nil {
		t.Fatal(err)
	}
	defer pdf.close()
	bitmap, err := pdf.render(100, 150, 0)
	if err != nil {
		t.Fatal(err)
	}
	if bitmap.contentHeight != 350 {
		t.Fatalf("cropped document height = %d, want 350", bitmap.contentHeight)
	}
	for _, tc := range []struct {
		y     int
		color uint32
	}{{50, 0xffff0000}, {125, 0xff00ff00}, {175, 0xff0000ff}} {
		if pixel := bitmap.pixels[tc.y*100+50]; pixel != tc.color {
			t.Fatalf("cropped document at y=%d: %#x, want %#x", tc.y, pixel, tc.color)
		}
	}
}

func TestContinuousRegionImmediateScrollAndDivider(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	buttons = 0
	t.Cleanup(func() { buttons = 0 })
	trm.displayRegions.jobs = make(chan regionJob, 1)
	s := trm.displayRegions.secondary
	r := trm.secondaryPixels()
	s.request = &term.RegionImage{Path: "preview.pdf", Continuous: true}
	s.reportScroll = true
	s.viewWidth = r.Dx()
	s.bitmap = regionBitmap{width: r.Dx(), height: 3 * r.Dy(), contentHeight: 1000, pixels: make([]uint32, r.Dx()*3*r.Dy())}
	for i := range s.bitmap.pixels {
		s.bitmap.pixels[i] = 0xff000000 | uint32(i/r.Dx())
	}
	s.dirty = true
	trm.requestRegionPaint()
	before, generation := framebuf[r.Min.Y*fbW+r.Min.X], trm.displayRegions.generation
	var input bytes.Buffer
	trm.termCore.SetWriter(func(b []byte) { input.Write(b) })
	trm.bpress(xproto.ButtonPressEvent{Detail: 5, EventX: int16(r.Min.X + 1), EventY: 2})
	trm.brelease(xproto.ButtonReleaseEvent{Detail: 5, EventX: int16(r.Min.X + 1), EventY: 2})
	if s.scrollY <= 0 || s.scrollY >= s.targetY || framebuf[r.Min.Y*fbW+r.Min.X] == before {
		t.Fatal("wheel did not immediately paint the first interpolated scroll frame")
	}
	if input.Len() != 0 || trm.displayRegions.generation != generation || len(trm.displayRegions.jobs) != 0 {
		t.Fatal("cached scrolling emitted input or re-decoded the PDF")
	}
	now := s.lastScroll
	for i := 1; i <= 20; i++ {
		trm.tickRegionScroll(now.Add(time.Duration(i) * 16 * time.Millisecond))
	}
	if s.scrollY != s.targetY {
		t.Fatal("scroll animation did not settle")
	}
	pane := trm.secondaryPanePixels()
	if r.Min.X-pane.Min.X != 3 {
		t.Fatal("splitter is not three pixels thick")
	}
	for y := pane.Min.Y; y < pane.Max.Y; y++ {
		for x := pane.Min.X; x < r.Min.X; x++ {
			if framebuf[y*fbW+x] != 0xff777777 {
				t.Fatal("splitter was overwritten")
			}
		}
	}
	trm.scrollRegionBy(10)
	job := <-trm.displayRegions.jobs
	trm.scrollRegionBy(2)
	if job.generation != trm.displayRegions.generation || len(trm.displayRegions.jobs) != 0 {
		t.Fatal("wheel burst invalidated or duplicated in-flight strip decoding")
	}
	trm.scrollRegionBy(-10000)
	if s.targetY != 0 {
		t.Fatal("scroll past document start did not clamp")
	}
}

func TestContinuousRegionWorkerCachesDocumentAndPreservesPosition(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	path := filepath.Join(t.TempDir(), "preview.pdf")
	if err := os.WriteFile(path, continuousTestPDF(""), 0600); err != nil {
		t.Fatal(err)
	}
	trm.mu.Lock()
	err := trm.OpenRegion(term.SecondaryRegion, term.RegionImage{Path: path, Continuous: true})
	trm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	await := func(check func(*regionSurface) bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			trm.mu.Lock()
			ready := check(trm.displayRegions.secondary)
			trm.mu.Unlock()
			if ready {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("continuous decoder did not complete")
	}
	await(func(s *regionSurface) bool { return s.bitmap.contentHeight > 0 && !s.loading })
	// Further strip requests must use the worker-owned PDF, even if its old
	// filename disappears during atomic replacement or temporary-file cleanup.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	trm.mu.Lock()
	err = trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "position", Value: 1000000})
	trm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	await(func(s *regionSurface) bool {
		return !s.loading && s.bitmap.top > 0 && s.scrollY == s.targetY && !s.scrollFailed
	})
	trm.mu.Lock()
	r := trm.secondaryPixels()
	if pixel := framebuf[(r.Max.Y-2)*fbW+r.Min.X+1]; pixel != 0xff0000ff {
		trm.mu.Unlock()
		t.Fatalf("scroll-to-end did not paint second page: %#x", pixel)
	}
	position := trm.displayRegions.secondary.scrollY
	trm.mu.Unlock()
	if err := os.WriteFile(path, continuousTestPDF(""), 0600); err != nil {
		t.Fatal(err)
	}
	trm.mu.Lock()
	_ = trm.OpenRegion(term.SecondaryRegion, term.RegionImage{Path: path, Continuous: true})
	trm.mu.Unlock()
	await(func(s *regionSurface) bool { return !s.loading && s.scrollY == position })
	trm.mu.Lock()
	_ = trm.ClearRegion(term.SecondaryRegion)
	if trm.displayRegions.secondary.bitmap.contentHeight != 0 {
		t.Error("clear retained continuous content")
	}
	trm.mu.Unlock()
}
