package frontend

import (
	"image"
	"sync"

	"st-go/limits"
	term "st-go/platform_neutral/stterminal"
)

// framebuffer sized to the window
var (
	fbW, fbH int
	framebuf []uint32
)

func ensureFramebuffer(w, h int) bool {
	size, valid := limits.RGBA("framebuffer", w, h)
	if !valid {
		return false
	}
	if len(framebuf) >= w*h && fbW == w && fbH == h {
		return true
	}
	framebuf = make([]uint32, size/4)
	fbW, fbH = w, h
	return true
}

func (t *Terminal) clearFramebuffer() {
	bg := t.colorAt(ColDefaultBg)
	for i := range framebuf {
		framebuf[i] = bg
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
	if fbW < 1 || fbH < 1 {
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
	key := glyphKey{u: u, fg: fg, bg: bg, w: w, h: t.ch, baseline: t.baseline}
	glyphMu.RLock()
	img, ok := glyphCache[key]
	glyphMu.RUnlock()
	if !ok {
		_, img = makeGlyph(u, fg, bg, w, t.ch, t.baseline)
		if len(img) == 0 {
			return 0, nil
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
	return w, img
}

// Clip at the final pixel write as well as at the logical terminal grid. In
// particular, a wide glyph/cursor at the divider must not touch the preview.
func (t *Terminal) blitTerminalPixels(px, py, w, h int, pixels []uint32) {
	r := image.Rect(px, py, px+w, py+h).Intersect(t.terminalClip())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		src := (y-py)*w + r.Min.X - px
		copy(framebuf[y*fbW+r.Min.X:y*fbW+r.Max.X], pixels[src:src+r.Dx()])
	}
}

// fillRect fills a region of the framebuffer with argb color.
func (t *Terminal) fillRect(argb uint32, px, py, w, h int) {
	if fbW < 1 || fbH < 1 {
		return
	}
	if w <= 0 || h <= 0 {
		return
	}
	r := image.Rect(px, py, px+w, py+h).Intersect(t.terminalClip())
	for yy := r.Min.Y; yy < r.Max.Y; yy++ {
		dst := framebuf[yy*fbW+r.Min.X : yy*fbW+r.Max.X]
		for i := range dst {
			dst[i] = argb
		}
	}
}

// term.Hooks implementation

func (t *Terminal) Bell()     {}
func (t *Terminal) ClipCopy() {}

func (t *Terminal) DrawLine(line []term.Glyph, x1, y1, x2 int) {
	// clear row first
	px0 := t.borderpx + x1*t.cw
	py := t.borderpx + y1*t.ch
	w := (x2 - x1) * t.cw
	t.fillRect(t.defaultBgARGB(), px0, py, w, t.ch)
	for x := x1; x < x2; x++ {
		g := line[x]
		// image cell: blit the raw image pixels (many colors per cell)
		if g.U == term.ImageRune {
			t.drawImageCell(g, x, y1)
			continue
		}
		if g.U == 0 || g.Mode&term.ATTRWdummy != 0 {
			continue
		}
		if t.termCore.Selected(x, y1) {
			g.Mode ^= term.ATTRReverse
		}
		fg := t.resolveFG(g)
		bg := t.resolveBG(g)
		wide := g.Mode&term.ATTRWide != 0
		t.drawGlyphAt(g.U, fg, bg, x, y1, wide)
	}
}

// FinishDraw is a no-op: paint.go submits the completed dirty row spans after the
// terminal core finishes rasterizing it.
func (t *Terminal) FinishDraw() {}
