package frontend

import (
	"image"
	"sync"

	"st-go/limits"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

// The working raster belongs to this terminal; its host retains committed
// pixels separately so synchronized output cannot expose half-painted state.
func (t *Terminal) ensureFramebuffer(w, h int) bool {
	size, valid := limits.RGBA("framebuffer", w, h)
	if !valid {
		return false
	}
	if t.scene() != nil {
		t.fbW, t.fbH = w, h
		t.framebuf = nil
		return true
	}
	if len(t.framebuf) >= w*h && t.fbW == w && t.fbH == h {
		return true
	}
	pixels := make([]uint32, size/4)
	for y := 0; y < min(h, t.fbH); y++ {
		copy(pixels[y*w:y*w+min(w, t.fbW)], t.framebuf[y*t.fbW:y*t.fbW+min(w, t.fbW)])
	}
	t.framebuf = pixels
	t.fbW, t.fbH = w, h
	return true
}

func (t *Terminal) clearFramebuffer() {
	bg := t.colorAt(ColDefaultBg)
	if s := t.scene(); s != nil {
		s.Apply(api.SceneUpdate{Reset: true, Background: bg})
		return
	}
	for i := range t.framebuf {
		t.framebuf[i] = bg
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Include raster dimensions so a cached cell cannot be reused at another size.
type glyphKey struct {
	font           uint64 // primary/fallback bundle and native render size
	u              rune
	fg, bg         uint32
	w, h, baseline int
}

// True-color output can produce a new key on every cell. Bound both pixel
// storage and map overhead instead of retaining every color seen forever.
const maxGlyphCacheBytes = limits.GlyphCacheBytes
const maxGlyphCacheEntries = limits.GlyphCacheEntries

var (
	glyphCache      = make(map[glyphKey][]uint32)
	glyphCacheBytes int
	glyphMu         sync.RWMutex
)

// drawGlyphAt renders rune u with fg/bg colors into the framebuffer at cell (x,y).
func (t *Terminal) drawGlyphAt(u rune, fg, bg uint32, x, y int, wide bool) {
	if t.fbW < 1 || t.fbH < 1 {
		return
	}
	w, img := t.glyphPixels(u, fg, bg, wide)
	px := t.borderpx + x*t.cw
	py := t.borderpx + y*t.ch
	t.blitTerminalPixels(px, py, w, t.ch, img)
}

func (t *Terminal) glyphPixels(u rune, fg, bg uint32, wide bool) (int, []uint32) {
	w := t.cw
	if wide {
		w *= 2
	}
	return w, cachedGlyphPixels(u, fg, bg, w, t.ch, t.baseline)
}

func cachedGlyphPixels(u rune, fg, bg uint32, w, h, baseline int) []uint32 {
	// FreeType owns mutable face slots. Serialize lookup identity and native
	// rasterization against font reloads and other terminal widgets.
	fontMu.Lock()
	defer fontMu.Unlock()
	key := glyphKey{font: ft.generation, u: u, fg: fg, bg: bg, w: w, h: h, baseline: baseline}
	glyphMu.RLock()
	img, ok := glyphCache[key]
	glyphMu.RUnlock()
	if !ok {
		_, img = makeGlyphLocked(u, fg, bg, w, h, baseline)
		if len(img) == 0 {
			return nil
		}
		glyphMu.Lock()
		if size := len(img) * 4; size <= maxGlyphCacheBytes {
			if _, exists := glyphCache[key]; !exists {
				if glyphCacheBytes+size > maxGlyphCacheBytes || len(glyphCache) >= maxGlyphCacheEntries {
					clear(glyphCache)
					glyphCacheBytes = 0
				}
				glyphCache[key] = img
				glyphCacheBytes += size
			}
		}
		glyphMu.Unlock()
	}
	return img
}

// Clip at the final pixel write as well as at the logical terminal grid. In
// particular, a wide glyph/cursor at the divider must not touch the preview.
func (t *Terminal) blitTerminalPixels(px, py, w, h int, pixels []uint32) {
	r := image.Rect(px, py, px+w, py+h).Intersect(t.terminalClip())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		src := (y-py)*w + r.Min.X - px
		copy(t.framebuf[y*t.fbW+r.Min.X:y*t.fbW+r.Max.X], pixels[src:src+r.Dx()])
	}
}

// fillRect fills a region of the framebuffer with argb color.
func (t *Terminal) fillRect(argb uint32, px, py, w, h int) {
	if t.fbW < 1 || t.fbH < 1 {
		return
	}
	if w <= 0 || h <= 0 {
		return
	}
	r := image.Rect(px, py, px+w, py+h).Intersect(t.terminalClip())
	for yy := r.Min.Y; yy < r.Max.Y; yy++ {
		dst := t.framebuf[yy*t.fbW+r.Min.X : yy*t.fbW+r.Max.X]
		for i := range dst {
			dst[i] = argb
		}
	}
}

// term.Hooks implementation

func (t *Terminal) Bell()     {}
func (t *Terminal) ClipCopy() {}

func (t *Terminal) DrawLine(line []term.Glyph, x1, y1, x2 int) {
	t.drawSceneLine(line, x1, y1, x2)
}

// FinishDraw is a no-op: paint.go submits the completed dirty row spans after the
// terminal core finishes rasterizing it.
func (t *Terminal) FinishDraw() {}
