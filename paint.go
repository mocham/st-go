package main

import (
	"github.com/BurntSushi/xgb/xproto"

	"st-go/term"
)

// attachTerm installs the synchronous frontend paint callback before PTY
// processing starts. Paint requests raised while parsing PTY output are left
// to that loop's latency/synchronized-output scheduler; every other trigger
// paints immediately in its caller's t.mu critical section.
func (t *Terminal) attachTerm(core *term.Term) {
	t.termCore = core
	core.SetPaintFn(func() {
		if t.inPTYWrite {
			t.ptyPaintRequested = true
			return
		}
		t.paint()
	})
}

func (t *Terminal) writeTerminalOutput(core *term.Term, data []byte) int {
	t.mu.Lock()
	t.inPTYWrite = true
	written := core.Twrite(data, false)
	t.inPTYWrite = false
	if t.ptyPaintRequested && !core.IsPaintPaused() {
		t.ptyPaintRequested = false
		t.paint()
	}
	t.mu.Unlock()
	return written
}

func (t *Terminal) paintTerminalOutput(core *term.Term) {
	t.mu.Lock()
	if !core.IsPaintPaused() {
		core.PaintDirty()
	}
	t.mu.Unlock()
}

// paint rasterizes and submits all queued cell damage. Callers hold t.mu, so
// terminal state, framebuffer state, resize, and X event painting stay in one
// serialization domain without a separate paint goroutine or snapshot copy.
func (t *Terminal) paint() {
	if t.closed() || t.termCore == nil || t.termCore.IsPaintPaused() {
		return
	}
	region := t.termCore.Draw()
	if region.Empty() {
		return
	}
	px, py, w, h := t.pixelRegion(region)
	if w < 1 || h < 1 {
		return
	}
	t.putFramebufferRegion(px, py, w, h)
}

func (t *Terminal) pixelRegion(r term.Region) (px, py, w, h int) {
	w = (r.X2 - r.X1 + 1) * t.cw
	h = (r.Y2 - r.Y1 + 1) * t.ch
	px = t.borderpx + r.X1*t.cw
	py = t.borderpx + r.Y1*t.ch
	if px < 0 {
		px = 0
	}
	if py < 0 {
		py = 0
	}
	if px+w > fbW {
		w = fbW - px
	}
	if py+h > fbH {
		h = fbH - py
	}
	if w < 1 || h < 1 || fbW < 1 || fbH < 1 {
		return 0, 0, 0, 0
	}
	return px, py, w, h
}

// putFramebufferRegion sends framebuffer rows directly to X in request-sized
// chunks. XGB copies each request before returning, so no retained snapshot is
// needed while terminal mutation remains serialized by t.mu.
func (t *Terminal) putFramebufferRegion(px, py, w, h int) {
	if w < 1 || h < 1 || len(framebuf) == 0 {
		return
	}
	rowsPer := (262144 - 28) / (w * 4)
	if rowsPer < 1 {
		rowsPer = 1
	}
	if rowsPer > h {
		rowsPer = h
	}
	for ypos := 0; ypos < h; ypos += rowsPer {
		chunkRows := rowsPer
		if ypos+chunkRows > h {
			chunkRows = h - ypos
		}
		var pixels []uint32
		if px == 0 && w == fbW {
			start := (py + ypos) * fbW
			pixels = framebuf[start : start+w*chunkRows]
		} else {
			pixels = make([]uint32, 0, w*chunkRows)
			for yy := 0; yy < chunkRows; yy++ {
				start := (py+ypos+yy)*fbW + px
				pixels = append(pixels, framebuf[start:start+w]...)
			}
		}
		xproto.PutImage(t.conn, xproto.ImageFormatZPixmap,
			xproto.Drawable(t.win), t.gc,
			uint16(w), uint16(chunkRows), int16(px), int16(py+ypos), 0, 24,
			uint32ToBytes(pixels))
	}
}
