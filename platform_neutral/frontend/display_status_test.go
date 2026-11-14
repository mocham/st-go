package frontend

import (
	"image"
	"strings"
	"testing"

	"st-go/platform_neutral/api"
	"st-go/term"
)

func TestRegionStatusRetainsPreviewAndResizes(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	original := trm.displayRegions.secondary
	full := trm.secondaryPanePixels()
	cols, rows := trm.cols, trm.rows
	content := term.RegionStatus{Items: []term.RegionStatusItem{{Text: "12345678 Ready", Color: 0xff5fd787}}}
	if err := trm.SetRegionStatus(term.SecondaryRegion, content); err != nil {
		t.Fatal(err)
	}
	if trm.cols != cols || trm.rows != rows || trm.displayRegions.secondary != original {
		t.Fatal("status replaced preview or resized editor")
	}
	if trm.secondaryPanePixels().Max.Y >= full.Max.Y || trm.displayRegions.statusBounds.Max.Y != full.Max.Y {
		t.Fatal("footer did not reserve preview space")
	}
	footer := trm.displayRegions.statusBounds.Min
	if !trm.regionMousePress(1, 0, footer.X, footer.Y) {
		t.Fatal("footer click leaked into the editor")
	}
	trm.regionMouseRelease(1)
	content.Items[0].Text = "mutated"
	if trm.displayRegions.status.items[0].Text == "mutated" {
		t.Fatal("status retained caller-owned data")
	}
	view := trm.secondaryPanePixels()
	if err := trm.SetRegionStatus(term.SecondaryRegion, term.RegionStatus{Items: []term.RegionStatusItem{{Text: "12345678 Inserted", Color: 0xffffffff}}}); err != nil {
		t.Fatal(err)
	}
	if trm.secondaryPanePixels() != view {
		t.Fatal("state-only update moved preview")
	}
	trm.applyDisplayLayout(30, 8)
	bar := trm.displayRegions.statusBounds
	if bar.Empty() || bar.Min.Y != trm.displayRegions.pane.Max.Y {
		t.Fatal("resize detached footer")
	}
	if err := trm.SetRegionStatus(term.SecondaryRegion, term.RegionStatus{}); err != nil {
		t.Fatal(err)
	}
	if !trm.displayRegions.statusBounds.Empty() || trm.displayRegions.status != nil {
		t.Fatal("clear retained footer")
	}
	trm.SetRegionStatus(term.SecondaryRegion, content)
	trm.SetRegions(nil)
	if trm.displayRegions.status != nil {
		t.Fatal("region reset retained footer")
	}
}

type statusSceneSurface struct {
	scene  api.SceneUpdate
	raster bool
	pixels []uint32
}

func (s *statusSceneSurface) Configure(image.Rectangle, uint32)            {}
func (s *statusSceneSurface) Present(pixels []uint32, _, _, _ int, _ bool) { s.pixels = pixels }
func (s *statusSceneSurface) Expose() bool                                 { return true }
func (s *statusSceneSurface) Close()                                       {}
func (s *statusSceneSurface) PresentScene(_ uint64, u api.SceneUpdate) bool {
	s.scene = u
	return !s.raster
}

func TestRegionStatusSemanticColorsAndOverflow(t *testing.T) {
	status := &regionStatusSurface{cw: 8, ch: 16, baseline: 12, items: []term.RegionStatusItem{
		{Text: "a Initial", Color: 0xffaaaaaa}, {Text: "b Processing", Color: 0xffffd75f}, {Text: "c Ready", Color: 0xff5fd787}, {Text: "d Inserted", Color: 0xffffffff},
	}}
	surface := &statusSceneSurface{}
	p := regionPresenter{status: surface}
	p.presentStatus(regionFrame{status: status, statusBounds: image.Rect(0, 0, 800, 20)})
	if len(surface.scene.Rows) != 1 || len(surface.scene.Rows[0].Text) != 4 {
		t.Fatal("missing status items")
	}
	for i, run := range surface.scene.Rows[0].Text {
		if run.Foreground != status.items[i].Color || run.Text != status.items[i].Text {
			t.Fatal("state color lost")
		}
	}
	// Widget surfaces implement SemanticSurface even on native hosts, where
	// PresentScene declines the update and requires the raster fallback.
	surface.raster = true
	p.presentStatus(regionFrame{status: status, statusBounds: image.Rect(0, 0, 800, 20)})
	if len(surface.pixels) != 800*20 || surface.pixels[0] != regionStatusBackground {
		t.Fatal("native footer was not painted")
	}
	u := regionStatusScene(status, 100, 20)
	var text strings.Builder
	for _, row := range u.Rows {
		for _, run := range row.Text {
			text.WriteString(run.Text)
		}
	}
	if !strings.Contains(text.String(), "+4 more") {
		t.Fatalf("overflow wasn't visible: %s", text.String())
	}
}
