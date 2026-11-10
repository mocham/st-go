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
	t.UploadAt(framebuf, stride, image.Rect(px, py, px+w, py+h), image.Pt(px, py))
}
func (t *Driver) UploadAt(framebuf []uint32, stride int, source image.Rectangle, destination image.Point) {
	if source.Empty() || stride < 1 || len(framebuf) == 0 || t.conn == nil {
		return
	}
	delta := destination.Sub(source.Min)
	source = source.Intersect(image.Rect(0, 0, stride, len(framebuf)/stride))
	if source.Empty() {
		return
	}
	px, py, w, h := source.Min.X+delta.X, source.Min.Y+delta.Y, source.Dx(), source.Dy()
	r := t.rememberCursorBaseAt(framebuf, stride, source, delta)
	rowsPer := max(1, (262144-28)/(w*4))
	rowsPer = min(rowsPer, h)
	for ypos := 0; ypos < h; ypos += rowsPer {
		chunkRows := min(rowsPer, h-ypos)
		var pixels []uint32
		if source.Min.X == 0 && w == stride {
			start := (source.Min.Y + ypos) * stride
			pixels = framebuf[start : start+w*chunkRows]
		} else {
			pixels = make([]uint32, 0, w*chunkRows)
			for yy := 0; yy < chunkRows; yy++ {
				start := (source.Min.Y+ypos+yy)*stride + source.Min.X
				pixels = append(pixels, framebuf[start:start+w]...)
			}
		}
		xproto.PutImage(t.conn, xproto.ImageFormatZPixmap, xproto.Drawable(t.win), t.gc,
			uint16(w), uint16(chunkRows), int16(px), int16(py+ypos), 0, 24, pixelBytes(pixels))
	}
	if !r.Empty() {
		t.putCursorPixels(t.textCursor.Bounds, t.textCursor.Pixels)
	}
}

// Keep only the clean cell underneath the caret, never another screen. Content
// updates must refresh this underlay before moving/hiding a stationary caret.
func (t *Driver) rememberCursorBase(pixels []uint32, stride int, damage image.Rectangle) image.Rectangle {
	return t.rememberCursorBaseAt(pixels, stride, damage, image.Point{})
}
func (t *Driver) rememberCursorBaseAt(pixels []uint32, stride int, source image.Rectangle, delta image.Point) image.Rectangle {
	r := source.Add(delta).Intersect(t.textCursor.Bounds)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		at := (y-t.textCursor.Bounds.Min.Y)*t.textCursor.Bounds.Dx() + r.Min.X - t.textCursor.Bounds.Min.X
		start := (y-delta.Y)*stride + r.Min.X - delta.X
		copy(t.textCursor.Base[at:at+r.Dx()], pixels[start:start+r.Dx()])
	}
	return r
}

func (t *Driver) putCursorPixels(bounds image.Rectangle, pixels []uint32) {
	if !bounds.Empty() && t.conn != nil {
		w := bounds.Dx()
		rows := max(1, (262144-28)/(w*4))
		for y := 0; y < bounds.Dy(); y += rows {
			n := min(rows, bounds.Dy()-y)
			xproto.PutImage(t.conn, xproto.ImageFormatZPixmap, xproto.Drawable(t.win), t.gc,
				uint16(w), uint16(n), int16(bounds.Min.X), int16(bounds.Min.Y+y), 0, 24, pixelBytes(pixels[y*w:(y+n)*w]))
		}
	}
}

func (t *Driver) SetTextCursor(cursor api.TextCursor) {
	if !cursor.Valid() {
		cursor = api.TextCursor{}
	}
	if t.textCursor.Equal(cursor) {
		return
	}
	t.putCursorPixels(t.textCursor.Bounds, t.textCursor.Base)
	t.textCursor.Bounds = cursor.Bounds
	t.textCursor.Pixels = append(t.textCursor.Pixels[:0], cursor.Pixels...)
	t.textCursor.Base = append(t.textCursor.Base[:0], cursor.Base...)
	t.putCursorPixels(cursor.Bounds, t.textCursor.Pixels)
}

type Surface struct {
	driver *Driver
	window xproto.Window
	pixmap xproto.Pixmap
	gc     xproto.Gcontext
	bounds image.Rectangle
	hidden bool
	kind   api.WidgetKind
}

func (t *Driver) NewSurface(bounds image.Rectangle, background uint32) (api.Surface, error) {
	return t.NewWidgetSurface(api.TerminalWidget, bounds, background)
}
func (t *Driver) NewWidgetSurface(kind api.WidgetKind, bounds image.Rectangle, background uint32) (api.Surface, error) {
	return t.NewWidgetSurfaceVisible(kind, bounds, background, true)
}
func (t *Driver) NewWidgetSurfaceVisible(kind api.WidgetKind, bounds image.Rectangle, background uint32, visible bool) (api.Surface, error) {
	win, err := xproto.NewWindowId(t.conn)
	if err != nil {
		return nil, err
	}
	gc, err := xproto.NewGcontextId(t.conn)
	if err != nil {
		return nil, err
	}
	p := &Surface{driver: t, window: win, gc: gc, bounds: bounds, kind: kind, hidden: !visible}
	// Keep pointer propagation and the existing isolated-preview drawable.
	xproto.CreateWindow(t.conn, 0, win, t.win, int16(bounds.Min.X), int16(bounds.Min.Y),
		uint16(bounds.Dx()), uint16(bounds.Dy()), 0, xproto.WindowClassInputOutput, 0,
		xproto.CwBackPixel|xproto.CwEventMask, []uint32{background & 0xffffff, xproto.EventMaskExposure})
	xproto.CreateGC(t.conn, gc, xproto.Drawable(win), xproto.GcGraphicsExposures, []uint32{0})
	if visible {
		xproto.MapWindow(t.conn, win)
	}
	t.surfaceMu.Lock()
	if t.surfaces == nil {
		t.surfaces = make(map[xproto.Window]*Surface)
	}
	t.surfaces[win] = p
	t.surfaceMu.Unlock()
	p.resetBacking()
	t.raiseBars()
	return p, nil
}

func (t *Driver) raiseBars() {
	t.surfaceMu.RLock()
	defer t.surfaceMu.RUnlock()
	for _, s := range t.surfaces {
		if s.kind == api.BarWidget && !s.hidden {
			xproto.ConfigureWindow(t.conn, s.window, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
		}
	}
}
func (p *Surface) SetVisible(visible bool) {
	if p.window == 0 || p.hidden != visible {
		return
	}
	p.hidden = !visible
	if visible {
		xproto.MapWindow(p.driver.conn, p.window)
		p.driver.raiseBars()
	} else {
		xproto.UnmapWindow(p.driver.conn, p.window)
	}
}

func (p *Surface) Configure(bounds image.Rectangle, background uint32) {
	old := p.bounds
	p.bounds = bounds
	xproto.ConfigureWindow(p.driver.conn, p.window,
		xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight,
		[]uint32{uint32(bounds.Min.X), uint32(bounds.Min.Y), uint32(bounds.Dx()), uint32(bounds.Dy())})
	if old.Size() != bounds.Size() {
		p.resetBacking()
	}
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
	if p.hidden || p.window == 0 {
		return
	}
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
	if p.pixmap == 0 || p.hidden {
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
