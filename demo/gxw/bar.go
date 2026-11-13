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
	"time"

	"github.com/jezek/xgb/xproto"
	xdraw "golang.org/x/image/draw"
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

func (w *wm) icon(kind byte) *image.Alpha {
	if kind == 0 {
		return nil
	}
	if w.icons == nil {
		w.icons = make(map[byte]*image.Alpha)
	}
	if mask := w.icons[kind]; mask != nil {
		return mask
	}
	source := systembar.Icon(kind)
	if source == nil {
		return nil
	}
	height := max(1, w.barHeight-8)
	width := max(1, height*systembar.IconWidth/systembar.IconHeight)
	mask := image.NewAlpha(image.Rect(0, 0, width, height))
	xdraw.BiLinear.Scale(mask, mask.Rect, source, source.Rect, draw.Src, nil)
	w.icons[kind] = mask
	return mask
}

func (w *wm) label(img *image.RGBA, r image.Rectangle, text string, fg color.RGBA) {
	r = r.Intersect(img.Rect)
	if r.Empty() {
		return
	}
	maxWidth := max(0, r.Dx()-16)
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
	d := font.Drawer{Dst: img.SubImage(r).(*image.RGBA), Src: image.NewUniform(fg), Face: w.face, Dot: fixed.P(r.Min.X+8, y)}
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
	draw.Draw(img, image.Rect(r.Max.X-1, r.Min.Y+5, r.Max.X, r.Max.Y-5), image.NewUniform(activeBG), image.Point{}, draw.Src)
	fg := barFG
	if c := w.clients[window]; c != nil && c.urgent {
		fg = urgentFG
	}
	w.label(img, r, label, fg)
	if action != "" {
		w.hits[id] = append(w.hits[id], hit{rect: r, action: action, window: window})
	}
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
		start, end, arrows := tabRange(len(w.tabs), slices.Index(w.tabs, w.active), w.width, w.barHeight)
		x, right := 0, w.width
		if arrows {
			w.button(top, w.tabbar, image.Rect(0, 0, w.barHeight, top.Rect.Dy()), "<", "prev", 0, false)
			w.button(top, w.tabbar, image.Rect(w.width-w.barHeight, 0, w.width, top.Rect.Dy()), ">", "next", 0, false)
			x = w.barHeight
			right -= w.barHeight
		}
		available := max(1, right-x)
		for i := start; i < end; i++ {
			left := x + (i-start)*available/(end-start)
			r := x + (i-start+1)*available/(end-start)
			id := w.tabs[i]
			name := fmt.Sprintf("%d  %s", i+1, w.clients[id].title)
			w.button(top, w.tabbar, image.Rect(left, 0, r, top.Rect.Dy()), name, "tab", id, id == w.active)
		}
	}
	w.upload(w.tabbar, top)
	bottom := image.NewRGBA(image.Rect(0, 0, w.width, w.height-a.Max.Y))
	draw.Draw(bottom, bottom.Rect, image.NewUniform(barBG), image.Point{}, draw.Src)
	x := w.width
	put := func(text, action string, icon byte) {
		width := font.MeasureString(w.face, text).Ceil() + 18
		mask := w.icon(icon)
		if mask != nil {
			width += mask.Rect.Dx()
			if text != "" {
				width += 4
			}
		}
		if x-width < 0 {
			return
		}
		r := image.Rect(x-width, 0, x, bottom.Rect.Dy())
		x -= width
		if mask == nil {
			w.button(bottom, w.systembar, r, text, action, 0, false)
		} else {
			w.button(bottom, w.systembar, r, "", action, 0, false)
			at := image.Pt(r.Min.X+8, r.Min.Y+(r.Dy()-mask.Rect.Dy())/2)
			draw.DrawMask(bottom, mask.Rect.Add(at), image.NewUniform(barFG), image.Point{}, mask, image.Point{}, draw.Over)
			if text != "" {
				r.Min.X += mask.Rect.Dx() + 4
				w.label(bottom, r, text, barFG)
			}
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
	w.label(bottom, image.Rect(0, 0, max(0, x-4), bottom.Rect.Dy()), text, barFG)
	w.upload(w.systembar, bottom)
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
			w.activate(h.window)
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
