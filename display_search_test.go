package main

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"st-go/limits"
	"st-go/term"
)

func occurrenceTestPDF() []byte {
	streams := []string{
		"BT /F1 12 Tf 20 550 Td (needle first) Tj 0 -180 Td (needle second) Tj ET\n",
		"BT /F1 12 Tf 20 500 Td (needle third) Tj ET\n",
	}
	objects := []string{"<</Type/Catalog/Pages 2 0 R>>", "<</Type/Pages/Kids[3 0 R 5 0 R]/Count 2>>"}
	for i, stream := range streams {
		objects = append(objects, fmt.Sprintf("<</Type/Page/Parent 2 0 R/MediaBox[0 0 400 600]/Resources<</Font<</F1 7 0 R>>>>/Contents %d 0 R>>", 4+2*i), fmt.Sprintf("<</Length %d>>\nstream\n%sendstream", len(stream), stream))
	}
	return pdfTestObjects(append(objects, "<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>"))
}

func TestNativePDFAllOccurrences(t *testing.T) {
	doc, err := openScrollPDF(occurrenceTestPDF())
	if err != nil {
		t.Fatal(err)
	}
	defer doc.close()
	matches, tops, err := doc.matches(400, "NEEDLE", func() bool { return true })
	if err != nil || matches == nil || len(matches.hits) != 3 {
		t.Fatalf("all occurrences = %+v, %v", matches, err)
	}
	if matches.hits[0].page != 0 || matches.hits[1].page != 0 || matches.hits[2].page != 1 || matches.hits[0].box.Min.Y >= matches.hits[1].box.Min.Y {
		t.Fatal("same-page occurrences were skipped or out of order")
	}
	for _, hit := range matches.hits {
		if hit.box.Empty() || !hit.box.In(image.Rect(0, 0, 1000001, 1000001)) {
			t.Fatalf("invalid hit rectangle: %+v", hit)
		}
	}
	matches.choose(0, 0, 1)
	if matches.active != 0 {
		t.Fatal("forward search did not select the first occurrence")
	}
	matches.choose(0, matches.hits[0].box.Max.Y, 1)
	if matches.active != 1 {
		t.Fatal("search from midway through a page did not find its next occurrence")
	}
	matches.choose(0, 0, -1)
	if matches.active != 2 {
		t.Fatal("backward search did not wrap")
	}
	page, y := matches.destination(tops, 100)
	if page != 2 || y < tops[1] {
		t.Fatal("occurrence destination used the wrong page geometry")
	}
	if result, _, err := doc.matches(400, "needle", func() bool { return false }); err != nil || result != nil {
		t.Fatal("cancelled search published partial results")
	}
}

func TestNativePDFOccurrenceBudget(t *testing.T) {
	doc, err := openScrollPDF(metadataActualTextPDF(strings.Repeat("needle ", limits.PreviewSearchMatches+2)))
	if err != nil {
		t.Fatal(err)
	}
	defer doc.close()
	matches, _, err := doc.matches(300, "needle", func() bool { return true })
	if err != nil || matches == nil || len(matches.hits) != limits.PreviewSearchMatches || !matches.truncated {
		t.Fatalf("match budget: matches=%+v, %v", matches, err)
	}
}

func TestNativePDFUnicodeOccurrences(t *testing.T) {
	doc, err := openScrollPDF(metadataActualTextPDF("cafe café CAFÉ α Α"))
	if err != nil {
		t.Fatal(err)
	}
	defer doc.close()
	for _, test := range []struct {
		query string
		count int
	}{{"cafe", 1}, {"café", 2}, {"cafe\u0301", 2}, {"α", 2}} {
		matches, _, err := doc.matches(300, test.query, func() bool { return true })
		if err != nil || matches == nil || len(matches.hits) != test.count {
			t.Fatalf("Unicode query %q: matches=%+v, %v", test.query, matches, err)
		}
	}
}

func TestContinuousRegionOccurrenceNavigationAndHighlights(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	file := filepath.Join(t.TempDir(), "occurrences.pdf")
	if err := os.WriteFile(file, occurrenceTestPDF(), 0600); err != nil {
		t.Fatal(err)
	}
	var replies bytes.Buffer
	trm.termCore.SetWriter(func(data []byte) { replies.Write(data) })
	trm.mu.Lock()
	err := trm.OpenRegion(term.SecondaryRegion, term.RegionImage{Path: file, Continuous: true})
	trm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	await := func(check func(*regionSurface) bool) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			trm.mu.Lock()
			trm.displayRegions.mu.Lock()
			ok := check(trm.displayRegions.secondary)
			trm.displayRegions.mu.Unlock()
			trm.mu.Unlock()
			if ok {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("native search/render did not settle")
	}
	await(func(s *regionSurface) bool { return len(s.bitmap.pageTops) == 3 && !s.loading })
	trm.mu.Lock()
	err = trm.FindRegion(term.SecondaryRegion, "needle", 1, false)
	if err == nil {
		// Layout changes invalidate bitmap generations, but must not lose a
		// search that has not published its occurrence list yet.
		err = trm.SetRegions([]term.DisplayRegion{{ID: term.MainRegion, Bounds: term.CellRect{Width: 10, Height: 5}}, {ID: term.SecondaryRegion, Bounds: term.CellRect{X: 10, Width: 10, Height: 5}}})
	}
	trm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	await(func(s *regionSurface) bool { return s.search != nil && len(s.search.hits) == 3 && !s.loading })
	for _, active := range []int{0, 1, 2, 0} {
		trm.mu.Lock()
		trm.displayRegions.mu.Lock()
		s := *trm.displayRegions.secondary
		if s.search.active != active {
			trm.displayRegions.mu.Unlock()
			trm.mu.Unlock()
			t.Fatalf("active occurrence = %d, want %d", s.search.active, active)
		}
		viewport, pane := trm.displayRegions.viewport, trm.displayRegions.pane
		trm.displayRegions.mu.Unlock()
		colored, plain := make([]uint32, len(framebuf)), make([]uint32, len(framebuf))
		renderRegionPixels(s, viewport, pane, 0xffffffff, colored, fbW)
		base := s
		base.search = nil
		renderRegionPixels(base, viewport, pane, 0xffffffff, plain, fbW)
		changed := false
		for i := range colored {
			if colored[i] != plain[i] {
				changed = true
				break
			}
		}
		if !changed {
			t.Error("search did not change visible framebuffer pixels")
		}
		trm.flushRegionReplies()
		if err := trm.FindRegion(term.SecondaryRegion, "needle", 1, true); err != nil {
			t.Error(err)
		}
		trm.mu.Unlock()
		await(func(s *regionSurface) bool { return !s.loading })
	}
	trm.mu.Lock()
	if !strings.Contains(replies.String(), "matches 1 2 3 2 0") {
		t.Error("same-page match index was not reported")
	}
	trm.displayRegions.mu.Lock()
	retained := trm.displayRegions.secondary.search
	trm.displayRegions.mu.Unlock()
	if err := trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "zoom", Value: 200}); err != nil {
		t.Error(err)
	}
	trm.mu.Unlock()
	await(func(s *regionSurface) bool { return !s.loading })
	trm.mu.Lock()
	trm.displayRegions.mu.Lock()
	preserved := trm.displayRegions.secondary.search == retained
	trm.displayRegions.mu.Unlock()
	if !preserved {
		t.Error("zoom discarded the cached occurrence list")
	}
	if err := trm.FindRegion(term.SecondaryRegion, "", 1, false); err != nil {
		t.Error(err)
	}
	trm.displayRegions.mu.Lock()
	cleared := trm.displayRegions.secondary.search == nil && trm.displayRegions.secondary.pendingSearch == nil
	trm.displayRegions.mu.Unlock()
	trm.mu.Unlock()
	if !cleared {
		t.Fatal("clearing search retained highlights")
	}
}

func TestPDFSearchOverlayScaleAndClip(t *testing.T) {
	viewport := image.Rect(2, 4, 34, 44)
	surface := regionSurface{zoom: 200, pan: 8, scrollY: 40,
		request: &term.RegionImage{Continuous: true},
		bitmap:  regionBitmap{width: 64, contentHeight: 128, pageTops: []int{0, 128}},
		search:  &pdfMatches{active: 0, hits: []pdfMatch{{page: 0, box: image.Rect(250000, 250000, 500000, 375000)}}},
	}
	pixels := make([]uint32, 40*48)
	for i := range pixels {
		pixels[i] = 0xffffffff
	}
	paintSearchMatches(surface, viewport, pixels, 40)
	want := image.Rect(10, 4, 26, 12)
	for y := 0; y < 48; y++ {
		for x := 0; x < 40; x++ {
			color := uint32(0xffffffff)
			if image.Pt(x, y).In(want) {
				color = 0xffffaa2d
			}
			if pixels[y*40+x] != color {
				t.Fatalf("zoom/pan/clipped highlight at %d,%d = %#x, want %#x", x, y, pixels[y*40+x], color)
			}
		}
	}
}
