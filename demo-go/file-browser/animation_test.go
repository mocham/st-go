package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"st-go/config"
	"st-go/term"
	"st-go/tui/navigation"
)

type animationHooks struct {
	term.Hooks
	frames int
}

func (*animationHooks) SetMode(bool, uint) {}
func (*animationHooks) SetCursor(int) bool { return false }
func (*animationHooks) StartDraw() bool    { return false }
func (*animationHooks) ImageClearAll()     {}
func (h *animationHooks) ImageDecodeAnim(_ []byte, opts term.ImageDecodeOptions) ([]int, int, int, int, bool) {
	return []int{100, 100, 100}, 3, 1, 1, true
}
func (h *animationHooks) ImageDecodeAnimFrame(_ []byte, index int, _ term.ImageDecodeOptions) (int, int, []term.Glyph, bool) {
	h.frames++
	return 1, 1, []term.Glyph{{U: rune('A' + index)}}, true
}

func TestAnimatedPreviewSurvivesSidebar(t *testing.T) {
	data, ok := makeAnimatedWebpBytes(t)
	if !ok {
		t.Skip("ffmpeg unavailable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "animation.webp")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"floating", "pinned", "hidden"} {
		t.Run(mode, func(t *testing.T) {
			var output bytes.Buffer
			b := &Browser{out: bufio.NewWriter(&output), rows: 24, cols: 80,
				listX: 47, listW: 34, listTop: 8, listBottom: 23, visible: 16,
				navigation: navigation.Panel{Visible: mode != "hidden", Pinned: mode == "pinned"},
				files:      []string{path}, previewLink: filepath.Join(dir, "preview")}
			b.updatePreviewWidth()
			b.renderPreview()
			b.drawOverlay()
			b.flush()
			hooks := &animationHooks{}
			core := term.NewTerm(config.Default(), hooks)
			core.Twrite(output.Bytes(), false)
			if !core.HasAnim() {
				t.Fatal("browser chrome cancelled playback after frame 1")
			}
			before := make([]term.Glyph, core.Cols()-46)
			for x := 46; x < core.Cols(); x++ {
				before[x-46] = core.LineAt(x, 3)
			}
			if !core.TickAnim(time.Now().Add(150*time.Millisecond)) || hooks.frames != 2 {
				t.Fatal("animation did not advance past frame 1")
			}
			if mode != "hidden" {
				for x := 46; x < core.Cols(); x++ {
					if core.LineAt(x, 3) != before[x-46] {
						t.Fatal("animation overwrote sidebar")
					}
				}
			}
		})
	}
}
