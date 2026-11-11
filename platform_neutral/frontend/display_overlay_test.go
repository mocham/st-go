package frontend

import (
	"image"
	"testing"

	term "st-go/platform_neutral/stterminal"
)

func TestPreviewOverlayPreservesGridAndClipsOnResize(t *testing.T) {
	trm := regionTestTerminal(t)
	resizes := 0
	trm.ttyResize = func(int, int) { resizes++ }
	trm.writeTerminalOutput(trm.termCore, []byte("before\x1bPregion overlay 1 3 12 2\x1b\\after"))
	if trm.cols != 20 || trm.rows != 5 || resizes != 0 || trm.termCore.CursorX() != 11 || trm.termCore.LineText(0)[:11] != "beforeafter" {
		t.Fatal("overlay changed PTY/grid/cursor/text state")
	}
	if got := trm.secondaryPixels(); got != image.Rect(1, 17, 49, 33) {
		t.Fatal("overlay pixels differ from its cell rectangle", got)
	}
	if trm.overSecondary(60, 20) || !trm.overSecondary(5, 20) {
		t.Fatal("overlay consumed sidebar input")
	}
	if err := trm.SetRegionOverlay(term.CellRect{X: 19, Width: 2, Height: 1}); err == nil {
		t.Fatal("accepted out-of-bounds overlay")
	}
	trm.applyDisplayLayout(8, 3)
	if got := trm.Regions()[1].Bounds; got != (term.CellRect{Y: 2, Width: 8, Height: 1}) {
		t.Fatal("region query reported geometry outside the resized grid", got)
	}
	if got := trm.secondaryPixels(); got != image.Rect(1, 17, 33, 25) {
		t.Fatal("overlay was not clipped after resize", got)
	}
	trm.applyDisplayLayout(20, 5)
	if err := trm.SetRegions(nil); err != nil || trm.displayRegions.secondary != nil || trm.displayRegions.overlay.Width != 0 {
		t.Fatal("reset retained overlay", err)
	}
	splitRegionTest(t, trm)
}
