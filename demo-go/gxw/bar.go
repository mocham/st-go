package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/jezek/xgb/xproto"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
	"st-go/widgets/systembar"
)

var (
	barBG    = systemColor(systembar.Background)
	barFG    = systemColor(systembar.Foreground)
	activeBG = color.RGBA{79, 76, 111, 255}
	urgentFG = color.RGBA{255, 201, 105, 255}
)

func systemColor(pixel uint32) color.RGBA {
	return color.RGBA{byte(pixel >> 16), byte(pixel >> 8), byte(pixel), byte(pixel >> 24)}
}

func (w *wm) label(img *image.RGBA, r image.Rectangle, text string, fg color.RGBA) {
	r = r.Intersect(img.Rect)
	if r.Empty() {
		return
	}
	maxWidth := r.Dx()
	if font.MeasureString(w.face, text).Ceil() > maxWidth {
		runes := []rune(text)
		// Binary search keeps a long application title from stalling redraws.
		lo, hi := 0, len(runes)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if font.MeasureString(w.face, string(runes[:mid])+"…").Ceil() <= maxWidth {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		text = string(runes[:lo]) + "…"
	}
	m := w.face.Metrics()
	y := r.Min.Y + (r.Dy()-m.Ascent.Ceil()-m.Descent.Ceil())/2 + m.Ascent.Ceil()
	d := font.Drawer{Dst: img.SubImage(r).(*image.RGBA), Src: image.NewUniform(fg), Face: w.face, Dot: fixed.P(r.Min.X, y)}
	d.DrawString(text)
}

func (w *wm) button(img *image.RGBA, id xproto.Window, r image.Rectangle, label, action string, window xproto.Window, active bool) {
	r = r.Intersect(img.Rect)
	if r.Empty() {
		return
	}
	bg := barBG
	if active {
		bg = activeBG
	}
	draw.Draw(img, r, image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(r.Max.X-w.scale, r.Min.Y, r.Max.X, r.Max.Y), image.NewUniform(activeBG), image.Point{}, draw.Src)
	fg := barFG
	if c := w.clients[window]; c != nil && c.urgent {
		fg = urgentFG
	}
	w.label(img, r, label, fg)
	if action != "" {
		w.hits[id] = append(w.hits[id], hit{rect: r, action: action, window: window})
	}
}

func (w *wm) compactTab(img *image.RGBA, r image.Rectangle, index int, id xproto.Window) {
	w.button(img, w.tabbar, r, "", "tab", id, id == w.active)
	icon := r.Inset(w.scale).Intersect(img.Rect)
	if icon.Empty() {
		return
	}
	fg := barFG
	if w.clients[id].urgent {
		fg = urgentFG
	}
	stroke := max(1, w.scale)
	for _, edge := range []image.Rectangle{
		image.Rect(icon.Min.X, icon.Min.Y, icon.Max.X, icon.Min.Y+stroke),
		image.Rect(icon.Min.X, icon.Max.Y-stroke, icon.Max.X, icon.Max.Y),
		image.Rect(icon.Min.X, icon.Min.Y, icon.Min.X+stroke, icon.Max.Y),
		image.Rect(icon.Max.X-stroke, icon.Min.Y, icon.Max.X, icon.Max.Y),
	} {
		draw.Draw(img, edge, image.NewUniform(fg), image.Point{}, draw.Src)
	}
	text := strconv.Itoa(index + 1)
	m := w.face.Metrics()
	x := r.Min.X + (r.Dx()-font.MeasureString(w.face, text).Ceil())/2
	y := r.Min.Y + (r.Dy()-m.Ascent.Ceil()-m.Descent.Ceil())/2 + m.Ascent.Ceil()
	d := font.Drawer{Dst: img.SubImage(icon).(*image.RGBA), Src: image.NewUniform(fg), Face: w.face, Dot: fixed.P(x, y)}
	d.DrawString(text)
}

func (w *wm) drawBars() {
	if w.tabbar == 0 || w.systembar == 0 || w.gc == 0 || w.face == nil {
		return
	}
	w.hits[w.tabbar] = nil
	w.hits[w.systembar] = nil
	a := w.workarea()
	top := image.NewRGBA(image.Rect(0, 0, w.width, a.Min.Y))
	draw.Draw(top, top.Rect, image.NewUniform(barBG), image.Point{}, draw.Src)
	if len(w.tabs) == 0 {
		w.label(top, top.Rect, "gxw  |  Alt+Enter: terminal  |  Alt+Shift+Q: end session", barFG)
	} else {
		widths := make([]int, len(w.tabs))
		for i, id := range w.tabs {
			widths[i] = max(120*w.scale, w.barHeight*4)
			if w.clients[id].compact {
				widths[i] = max(w.barHeight, font.MeasureString(w.face, strconv.Itoa(i+1)).Ceil()+2*w.scale)
			}
		}
		start, end, arrows := tabRange(widths, slices.Index(w.tabs, w.active), w.width, w.barHeight)
		x, right := 0, w.width
		if arrows {
			w.button(top, w.tabbar, image.Rect(0, 0, w.barHeight, top.Rect.Dy()), "<", "prev", 0, false)
			w.button(top, w.tabbar, image.Rect(w.width-w.barHeight, 0, w.width, top.Rect.Dy()), ">", "next", 0, false)
			x = w.barHeight
			right -= w.barHeight
		}
		extra, expanded := max(1, right-x), 0
		for i := start; i < end; i++ {
			extra -= widths[i]
			if !w.clients[w.tabs[i]].compact {
				expanded++
			}
		}
		extra = max(0, extra)
		for i := start; i < end; i++ {
			id := w.tabs[i]
			width := widths[i]
			if !w.clients[id].compact && expanded > 0 {
				share := extra / expanded
				width += share
				extra -= share
				expanded--
			}
			r := image.Rect(x, 0, min(right, x+width), top.Rect.Dy())
			x = r.Max.X
			if w.clients[id].compact {
				w.compactTab(top, r, i, id)
				continue
			}
			name := fmt.Sprintf("%d  %s", i+1, w.clients[id].title)
			control := min(w.barHeight, r.Dx()/3)
			label := r
			label.Max.X -= control
			w.button(top, w.tabbar, label, name, "tab", id, id == w.active)
			w.button(top, w.tabbar, image.Rect(label.Max.X, r.Min.Y, r.Max.X, r.Max.Y), "-", "shrink", id, id == w.active)
		}
	}
	w.upload(w.tabbar, top)
	bottom := image.NewRGBA(image.Rect(0, 0, w.width, w.height-a.Max.Y))
	draw.Draw(bottom, bottom.Rect, image.NewUniform(barBG), image.Point{}, draw.Src)
	x := w.width
	put := func(text, action string, icon byte) {
		glyphs := []rune(text)
		if icon != 0 {
			if text != "" {
				glyphs = append([]rune{rune(icon), ' '}, glyphs...)
			} else {
				glyphs = []rune{rune(icon)}
			}
		}
		width := len(glyphs)*w.systemStyle.CellWidth + 2*w.systemStyle.Padding
		if x-width < 0 {
			return
		}
		r := image.Rect(x-width, 0, x, bottom.Rect.Dy())
		x -= width
		w.systemLabel(bottom, r, glyphs)
		if action != "" {
			w.hits[w.systembar] = append(w.hits[w.systembar], hit{rect: r, action: action})
		}
	}
	// Clock and terminal button have priority on small screens.
	put("", "terminal", systembar.NewTerminalIcon)
	now := w.status.sampledAt
	if now.IsZero() {
		now = time.Now()
	}
	put(now.Format("2006-01-02 15:04:05"), "", 0)
	if w.status.battery >= 0 {
		put(fmt.Sprintf("%d%%", w.status.battery), "", systembar.BatteryIcon)
	}
	put("", "shot", systembar.ScreenshotIcon)
	if w.status.brightness >= 0 {
		put("", "bright+", systembar.BrightnessUpIcon)
		put(fmt.Sprintf("%d", w.status.brightness), "", 0)
		put("", "bright-", systembar.BrightnessDownIcon)
	}
	if w.status.volume >= 0 {
		put("", "vol+", systembar.VolumeUpIcon)
		text := fmt.Sprintf("%d%%", w.status.volume)
		if w.status.muted {
			text = "Mute"
		}
		put(text, "mute", 0)
		put("", "vol-", systembar.VolumeDownIcon)
	}
	text := "gxw"
	if time.Now().Before(w.messageUntil) {
		text = w.message
	}
	w.systemLabel(bottom, image.Rect(0, 0, max(0, x), bottom.Rect.Dy()), []rune(text))
	w.upload(w.systembar, bottom)
}

func (w *wm) systemLabel(img *image.RGBA, r image.Rectangle, text []rune) {
	r = r.Intersect(img.Rect)
	s := w.systemStyle
	x, y := r.Min.X+s.Padding, r.Min.Y+s.Padding
	for _, ch := range text {
		if x+s.CellWidth > r.Max.X-s.Padding {
			break
		}
		if ch < 0 || ch >= 256 || s.Glyphs[ch] == nil {
			ch = '?'
		}
		glyph := s.Glyphs[ch]
		for row := 0; row < s.CellHeight && y+row < r.Max.Y; row++ {
			for col := 0; col < s.CellWidth; col++ {
				img.SetRGBA(x+col, y+row, systemColor(glyph[row*s.CellWidth+col]))
			}
		}
		x += s.CellWidth
	}
}

func (w *wm) barClick(e xproto.ButtonPressEvent) {
	if e.Event == w.tabbar && (e.Detail == 4 || e.Detail == 5) {
		delta := 1
		if e.Detail == 4 {
			delta = -1
		}
		w.cycle(delta)
		return
	}
	if e.Detail != 1 {
		return
	}
	for _, h := range w.hits[e.Event] {
		if !image.Pt(int(e.EventX), int(e.EventY)).In(h.rect) {
			continue
		}
		switch h.action {
		case "tab":
			if c := w.clients[h.window]; c != nil {
				c.compact = false
			}
			w.activate(h.window)
		case "shrink":
			w.shrinkTab(h.window)
		case "next":
			w.cycle(1)
		case "prev":
			w.cycle(-1)
		case "terminal":
			w.launchTerminal()
		case "shot":
			w.screenshot()
		default:
			w.systemAction(h.action)
		}
		return
	}
}

func (w *wm) screenshot() {
	if w.screenshotBusy {
		return
	}
	r, err := xproto.GetImage(w.conn, xproto.ImageFormatZPixmap, xproto.Drawable(w.root), 0, 0, uint16(w.width), uint16(w.height), 0xffffffff).Reply()
	if err != nil {
		w.notice(err.Error())
		return
	}
	img, err := w.pixels.decode(r.Data, w.width, w.height)
	if err != nil {
		w.notice(err.Error())
		return
	}
	w.screenshotBusy = true
	path := w.options.screenshotPath
	go func() {
		f, err := os.CreateTemp(filepath.Dir(path), ".gxw-screenshot-*.png")
		if err == nil {
			defer os.Remove(f.Name())
			err = png.Encode(f, img)
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(f.Name(), path)
			}
		}
		message := "Saved " + path
		if err != nil {
			message = err.Error()
		}
		w.screenshots <- message
	}()
}
