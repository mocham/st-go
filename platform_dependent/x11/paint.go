package x11

import (
	"image"
	"unsafe"

	"github.com/BurntSushi/xgb/xproto"
	"st-go/limits"
	"st-go/platform_neutral/api"
)

func pixelBytes(v []uint32) []byte {
	return (*[1 << 30]byte)(unsafe.Pointer(&v[0]))[: len(v)*4 : len(v)*4]
}

// Upload preserves the previous PutImage chunking and copies each request
// before returning; the host may immediately reuse its framebuffer.
func (t *Driver) Upload(framebuf []uint32, stride, px, py, w, h int) {
	if w < 1 || h < 1 || len(framebuf) == 0 || t.conn == nil {
		return
	}
	rowsPer := max(1, (262144-28)/(w*4))
	rowsPer = min(rowsPer, h)
	for ypos := 0; ypos < h; ypos += rowsPer {
		chunkRows := min(rowsPer, h-ypos)
		var pixels []uint32
		if px == 0 && w == stride {
			start := (py + ypos) * stride
			pixels = framebuf[start : start+w*chunkRows]
		} else {
			pixels = make([]uint32, 0, w*chunkRows)
			for yy := 0; yy < chunkRows; yy++ {
				start := (py+ypos+yy)*stride + px
				pixels = append(pixels, framebuf[start:start+w]...)
			}
		}
		xproto.PutImage(t.conn, xproto.ImageFormatZPixmap, xproto.Drawable(t.win), t.gc,
			uint16(w), uint16(chunkRows), int16(px), int16(py+ypos), 0, 24, pixelBytes(pixels))
	}
}

type Surface struct {
	driver *Driver
	window xproto.Window
	pixmap xproto.Pixmap
	gc     xproto.Gcontext
	bounds image.Rectangle
}

func (t *Driver) NewSurface(bounds image.Rectangle, background uint32) (api.Surface, error) {
	win, err := xproto.NewWindowId(t.conn)
	if err != nil {
		return nil, err
	}
	gc, err := xproto.NewGcontextId(t.conn)
	if err != nil {
		return nil, err
	}
	p := &Surface{driver: t, window: win, gc: gc, bounds: bounds}
	// Keep pointer propagation and the existing isolated-preview drawable.
	xproto.CreateWindow(t.conn, 0, win, t.win, int16(bounds.Min.X), int16(bounds.Min.Y),
		uint16(bounds.Dx()), uint16(bounds.Dy()), 0, xproto.WindowClassInputOutput, 0,
		xproto.CwBackPixel|xproto.CwEventMask, []uint32{background & 0xffffff, xproto.EventMaskExposure})
	xproto.CreateGC(t.conn, gc, xproto.Drawable(win), xproto.GcGraphicsExposures, []uint32{0})
	xproto.MapWindow(t.conn, win)
	t.surfaceMu.Lock()
	if t.surfaces == nil {
		t.surfaces = make(map[xproto.Window]*Surface)
	}
	t.surfaces[win] = p
	t.surfaceMu.Unlock()
	p.resetBacking()
	return p, nil
}

func (p *Surface) Configure(bounds image.Rectangle, background uint32) {
	p.bounds = bounds
	xproto.ConfigureWindow(p.driver.conn, p.window,
		xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight,
		[]uint32{uint32(bounds.Min.X), uint32(bounds.Min.Y), uint32(bounds.Dx()), uint32(bounds.Dy())})
	p.resetBacking()
}

func (p *Surface) resetBacking() {
	conn := p.driver.conn
	if p.pixmap != 0 {
		xproto.FreePixmap(conn, p.pixmap)
		p.pixmap = 0
	}
	pid, err := xproto.NewPixmapId(conn)
	if err == nil {
		p.pixmap = pid
		xproto.CreatePixmap(conn, 24, pid, xproto.Drawable(p.window), uint16(p.bounds.Dx()), uint16(p.bounds.Dy()))
	} else {
		limits.Report("preview pixmap", "%v", err)
	}
}

func (p *Surface) Present(pixels []uint32, w, h, dy int, reusable bool) {
	conn := p.driver.conn
	drawable := xproto.Drawable(p.window)
	start, end := 0, h
	if p.pixmap != 0 {
		drawable = xproto.Drawable(p.pixmap)
		if reusable && dy > -h && dy < h {
			switch {
			case dy > 0:
				xproto.CopyArea(conn, drawable, drawable, p.gc, 0, int16(dy), 0, 0, uint16(w), uint16(h-dy))
				start = h - dy
			case dy < 0:
				xproto.CopyArea(conn, drawable, drawable, p.gc, 0, 0, 0, int16(-dy), uint16(w), uint16(h+dy))
				end = -dy
			default:
				end = 0
			}
		}
	}
	rows := max(1, (262144-28)/(w*4))
	for y := start; y < end; y += rows {
		n := min(rows, end-y)
		xproto.PutImage(conn, xproto.ImageFormatZPixmap, drawable, p.gc, uint16(w), uint16(n), 0, int16(y), 0, 24, pixelBytes(pixels[y*w:(y+n)*w]))
	}
	if p.pixmap != 0 {
		xproto.CopyArea(conn, drawable, xproto.Drawable(p.window), p.gc, 0, 0, 0, 0, uint16(w), uint16(h))
	}
}

func (p *Surface) Expose() bool {
	if p.pixmap == 0 {
		return false
	}
	xproto.CopyArea(p.driver.conn, xproto.Drawable(p.pixmap), xproto.Drawable(p.window), p.gc, 0, 0, 0, 0, uint16(p.bounds.Dx()), uint16(p.bounds.Dy()))
	return true
}

func (p *Surface) Close() {
	t := p.driver
	if p.pixmap != 0 {
		xproto.FreePixmap(t.conn, p.pixmap)
		p.pixmap = 0
	}
	if p.window != 0 {
		t.surfaceMu.Lock()
		delete(t.surfaces, p.window)
		t.surfaceMu.Unlock()
		xproto.FreeGC(t.conn, p.gc)
		xproto.DestroyWindow(t.conn, p.window)
		p.window, p.gc = 0, 0
	}
}

// WindowID exposes the concrete drawable only to X-specific diagnostics/tests.
func (p *Surface) WindowID() xproto.Window { return p.window }
