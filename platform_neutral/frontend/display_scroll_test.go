package frontend

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
		"0 1 0 rg 10 0 90 400 re f 1 0 0 rg 10 300 90 100 re f\n",
		"0 0 1 rg 10 0 90 200 re f\n",
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
	return pdfTestObjects(objects)
}

func pdfTestObjects(objects []string) []byte {
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

func TestNativePDFPageSearch(t *testing.T) {
	stream := "BT /F1 12 Tf 10 150 Td (Needle text) Tj ET\n"
	data := pdfTestObjects([]string{
		"<</Type/Catalog/Pages 2 0 R>>", "<</Type/Pages/Kids[3 0 R 4 0 R]/Count 2>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 100 400]>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 100 200]/Resources<</Font<</F1 6 0 R>>>>/Contents 5 0 R>>",
		fmt.Sprintf("<</Length %d>>\nstream\n%sendstream", len(stream), stream),
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
	})
	pdf, err := openScrollPDF(data)
	if err != nil {
		t.Fatal(err)
	}
	defer pdf.close()
	for _, search := range []regionSearch{{text: "needle", page: 0, direction: 1}, {text: "NEEDLE", page: 0, direction: -1}, {text: "Needle", page: 2, direction: 1}} {
		page, y, err := pdf.find(100, &search, func() bool { return true })
		if err != nil || page != 2 || y < 430 || y > 460 {
			t.Fatalf("search=%+v page=%d y=%d err=%v", search, page, y, err)
		}
	}
	page, _, err := pdf.find(100, &regionSearch{text: "absent", direction: 1}, func() bool { return true })
	if err != nil || page != 0 {
		t.Fatal("absent text found")
	}
	page, _, err = pdf.find(100, &regionSearch{text: "Needle", direction: 1}, func() bool { return false })
	if err != nil || page != 0 {
		t.Fatal("cancelled search continued")
	}
}

func TestNativePDFUnembeddedStandardFonts(t *testing.T) {
	for _, name := range []string{"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique", "Times-Roman", "Times-Bold", "Times-Italic", "Times-BoldItalic", "Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique"} {
		t.Run(name, func(t *testing.T) {
			stream := "BT /F1 20 Tf 10 40 Td (Visible text) Tj ET\n"
			data := pdfTestObjects([]string{
				"<</Type/Catalog/Pages 2 0 R>>", "<</Type/Pages/Kids[3 0 R]/Count 1>>",
				"<</Type/Page/Parent 2 0 R/MediaBox[0 0 160 80]/Resources<</Font<</F1 5 0 R>>>>/Contents 4 0 R>>",
				fmt.Sprintf("<</Length %d>>\nstream\n%sendstream", len(stream), stream),
				fmt.Sprintf("<</Type/Font/Subtype/Type1/BaseFont/%s>>", name),
			})
			pdf, err := openScrollPDF(data)
			if err != nil {
				t.Fatal(err)
			}
			defer pdf.close()
			bitmap, err := pdf.render(160, 80, 0, 0xffffffff)
			if err != nil {
				t.Fatal(err)
			}
			ink := 0
			for _, pixel := range bitmap.pixels {
				if pixel&0xffffff < 0x808080 {
					ink++
				}
			}
			if ink < 100 {
				t.Fatalf("non-embedded %s text disappeared: %d dark pixels", name, ink)
			}
		})
	}
}

func TestContinuousPageNavigationZoomAndStatus(t *testing.T) {
	trm := regionTestTerminal(t)
	trm.writeTerminalOutput(trm.termCore, []byte("\x1bPregion stack 1\x1b\\"))
	trm.displayRegions.jobs = make(chan regionJob, 1)
	pdf, err := openScrollPDF(continuousTestPDF(""))
	if err != nil {
		t.Fatal(err)
	}
	defer pdf.close()
	_ = trm.OpenRegion(term.SecondaryRegion, term.RegionImage{Path: "a.pdf", Continuous: true, Background: 0xfff7f5ef})
	decode := func() {
		t.Helper()
		job := <-trm.displayRegions.jobs
		bitmap, err := pdf.render(job.width, job.height, job.position, job.background)
		if err != nil {
			t.Fatal(err)
		}
		trm.acceptRegionImage(job, bitmap, nil)
		trm.paint()
	}
	// A page jump received during initial decoding must resolve against actual
	// mixed-size page offsets when the first bitmap arrives.
	_ = trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "page", Value: 2})
	decode()
	decode()
	s := trm.displayRegions.secondary
	if s.currentPage() != 2 || s.scrollY != 320 {
		t.Fatalf("page jump: page=%d y=%g", s.currentPage(), s.scrollY)
	}
	var reply bytes.Buffer
	trm.termCore.SetWriter(func(b []byte) { reply.Write(b) })
	_ = trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "info"})
	if !bytes.Contains(reply.Bytes(), []byte("status 2 2 ")) {
		t.Fatalf("status: %q", reply.String())
	}
	// Background prefetch must not show "loading" while the visible area is
	// cached. A genuinely missing viewport must still report it.
	s.loading = true
	reply.Reset()
	_ = trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "info"})
	if !bytes.HasSuffix(reply.Bytes(), []byte("100 0 0\x1b\\")) {
		t.Fatalf("prefetch advertised foreground loading: %q", reply.String())
	}
	position := s.scrollY
	s.scrollY = float64(s.bitmap.top - 1)
	reply.Reset()
	_ = trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "info"})
	if !bytes.HasSuffix(reply.Bytes(), []byte("100 1 0\x1b\\")) {
		t.Fatalf("missing viewport did not advertise loading: %q", reply.String())
	}
	s.scrollY, s.loading = position, false
	if err := trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "zoom", Value: 501}); err == nil {
		t.Fatal("unbounded zoom accepted")
	}
	_ = trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "zoom", Value: 0})
	decode()
	if s.zoomPercent() != 20 || s.bitmap.width != 16 {
		t.Fatalf("best-fit: zoom=%d width=%d", s.zoomPercent(), s.bitmap.width)
	}
	r := trm.secondaryPixels()
	if framebuf[r.Min.Y*fbW+r.Min.X] != 0xfff7f5ef {
		t.Fatal("zoomed-out page was not centered on matching paper")
	}
	_ = trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "zoom", Value: 200})
	decode()
	_ = trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "pan", Value: 999})
	if s.pan != 80 {
		t.Fatal("horizontal pan did not clamp")
	}
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
		bitmap, err := pdf.render(100, 80, tc.position, 0xfff7f5ef)
		if err != nil {
			t.Fatal(err)
		}
		if bitmap.contentHeight != 600 || bitmap.height != limits.PreviewScreens*80 || len(bitmap.pixels)*4 > limits.PreviewCacheBytes {
			t.Fatalf("unbounded or incorrect strip geometry: %dx%d, total=%d", bitmap.width, bitmap.height, bitmap.contentHeight)
		}
		if got := bitmap.pixels[(tc.y-bitmap.top)*bitmap.width+50]; got != tc.color {
			t.Fatalf("document pixel at y=%d: %#x, want %#x (strip top %d)", tc.y, got, tc.color, bitmap.top)
		}
		if got := bitmap.pixels[(tc.y-bitmap.top)*bitmap.width]; got != 0xfff7f5ef {
			t.Fatalf("paper color at y=%d: %#x, want warm off-white", tc.y, got)
		}
	}
	resized, err := pdf.render(200, 80, 820, 0xffeaf2f8)
	if err != nil || resized.contentHeight != 1200 {
		t.Fatalf("fit-width resize failed: height=%d, err=%v", resized.contentHeight, err)
	}
	if resized.pixels[0] != 0xffeaf2f8 {
		t.Fatal("resized strip lost its custom paper color")
	}
	if len(resized.pageTops) != 3 || resized.pageTops[1] != 800 || resized.pageTops[2] != 1200 {
		t.Fatalf("mixed-size page offsets: %v", resized.pageTops)
	}
}

func TestContinuousPDFCropBoxKeepsContentAndJoinsPages(t *testing.T) {
	pdf, err := openScrollPDF(continuousTestPDF("/CropBox[0 250 100 400]"))
	if err != nil {
		t.Fatal(err)
	}
	defer pdf.close()
	bitmap, err := pdf.render(100, 150, 0, 0xffffffff)
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
	trm.paint()
	before, generation := framebuf[r.Min.Y*fbW+r.Min.X], trm.displayRegions.generation
	var input bytes.Buffer
	trm.termCore.SetWriter(func(b []byte) { input.Write(b) })
	trm.bpress(xproto.ButtonPressEvent{Detail: 5, EventX: int16(r.Min.X + 1), EventY: 2})
	trm.brelease(xproto.ButtonReleaseEvent{Detail: 5, EventX: int16(r.Min.X + 1), EventY: 2})
	trm.paint()
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
	err := trm.OpenRegion(term.SecondaryRegion, term.RegionImage{Path: path, Continuous: true, Background: 0xfff7f5ef})
	trm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	await := func(check func(*regionSurface) bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			trm.mu.Lock()
			trm.displayRegions.mu.Lock()
			ready := check(trm.displayRegions.secondary)
			trm.displayRegions.mu.Unlock()
			trm.paint()
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
		return !s.loading && s.scrollY > 0 && s.scrollY == s.targetY && !s.scrollFailed
	})
	trm.mu.Lock()
	r := trm.secondaryPixels()
	if pixel := framebuf[(r.Max.Y-2)*fbW+r.Min.X+r.Dx()/2]; pixel != 0xff0000ff {
		trm.mu.Unlock()
		t.Fatalf("scroll-to-end did not paint second page: %#x", pixel)
	}
	if framebuf[(r.Max.Y-2)*fbW+r.Min.X] != 0xfff7f5ef {
		t.Error("scroll prefetch lost the paper color")
	}
	trm.displayRegions.mu.Lock()
	position := trm.displayRegions.secondary.scrollY
	trm.displayRegions.mu.Unlock()
	trm.mu.Unlock()
	// Zoom/resize must reuse the native document, not reopen its filename.
	trm.mu.Lock()
	err = trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "zoom", Value: 200})
	trm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	await(func(s *regionSurface) bool { return !s.loading && s.bitmap.width == 2*r.Dx() && !s.scrollFailed })
	trm.mu.Lock()
	_ = trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "zoom", Value: 100})
	trm.mu.Unlock()
	await(func(s *regionSurface) bool { return !s.loading && s.bitmap.width == r.Dx() && !s.scrollFailed })
	if err := os.WriteFile(path, continuousTestPDF(""), 0600); err != nil {
		t.Fatal(err)
	}
	trm.mu.Lock()
	_ = trm.OpenRegion(term.SecondaryRegion, term.RegionImage{Path: path, Continuous: true, Background: 0xfff7f5ef})
	trm.mu.Unlock()
	await(func(s *regionSurface) bool { return !s.loading && s.scrollY == position })
	// A very narrow fit-width view makes this document shorter than the pane.
	// The canvas below it must have exactly the same color as the PDF paper.
	trm.mu.Lock()
	err = trm.SetRegions([]term.DisplayRegion{
		{ID: term.MainRegion, Bounds: term.CellRect{Width: 19, Height: 5}},
		{ID: term.SecondaryRegion, Bounds: term.CellRect{X: 19, Width: 1, Height: 5}},
	})
	trm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	await(func(s *regionSurface) bool { return !s.loading && s.bitmap.width == 1 })
	trm.mu.Lock()
	r = trm.secondaryPixels()
	if framebuf[(r.Max.Y-1)*fbW+r.Min.X] != 0xfff7f5ef {
		t.Error("canvas below the document lost the paper color")
	}
	_ = trm.ClearRegion(term.SecondaryRegion)
	trm.displayRegions.mu.Lock()
	if trm.displayRegions.secondary.bitmap.contentHeight != 0 {
		t.Error("clear retained continuous content")
	}
	trm.displayRegions.mu.Unlock()
	trm.mu.Unlock()
}
