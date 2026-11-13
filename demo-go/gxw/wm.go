package main

import (
	"context"
	"fmt"
	"image"
	"log"
	"slices"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/image/font"
	"st-go/widgets/systembar"
)

// All WM state and all X requests belong to run's goroutine. The event reader
// only transports events; system polling/commands return immutable results.
type wm struct {
	xim                              *ximServer
	options                          options
	conn                             *xgb.Conn
	screen                           *xproto.ScreenInfo
	root, support, tabbar, systembar xproto.Window
	gc                               xproto.Gcontext
	atoms                            map[string]xproto.Atom
	selection                        xproto.Atom
	owned                            bool
	width, height, barHeight         int
	scale                            int
	fonts                            *fontSource
	face                             font.Face
	pixels                           pixelFormat
	clients                          map[xproto.Window]*client
	order, tabs                      []xproto.Window
	active, focused                  xproto.Window
	timestamp                        xproto.Timestamp
	keys                             map[xproto.Keycode]uint32
	ignoredMods                      uint16
	hits                             map[xproto.Window][]hit
	systemStyle                      *systembar.Style
	systemScale                      float64
	status                           systemStatus
	jobs                             chan string
	message                          string
	messageUntil                     time.Time
	quit                             bool
	screenshotBusy                   bool
	screenshots                      chan string
}

func newWM(o options) (*wm, error) {
	conn, err := xgb.NewConnDisplay(o.display)
	if err != nil {
		return nil, fmt.Errorf("connect to X11: %w", err)
	}
	w := &wm{options: o, conn: conn, screen: xproto.Setup(conn).DefaultScreen(conn), atoms: make(map[string]xproto.Atom), clients: make(map[xproto.Window]*client), hits: make(map[xproto.Window][]hit), jobs: make(chan string, 16), screenshots: make(chan string, 1), status: systemStatus{battery: -1, volume: -1, brightness: -1}}
	ok := false
	defer func() {
		if !ok {
			w.close()
		}
	}()
	w.root = w.screen.Root
	w.width, w.height = int(w.screen.WidthInPixels), int(w.screen.HeightInPixels)
	w.pixels, err = newPixelFormat(xproto.Setup(conn), w.screen)
	if err != nil {
		return nil, err
	}
	w.fonts, err = configuredFonts(o)
	if err != nil {
		return nil, err
	}
	if err := w.updateScale(); err != nil {
		return nil, err
	}
	for _, name := range atomNames {
		if _, err = w.intern(name); err != nil {
			return nil, err
		}
	}
	w.selection, err = w.intern(fmt.Sprintf("WM_S%d", conn.DefaultScreen))
	if err != nil {
		return nil, err
	}
	owner, err := xproto.GetSelectionOwner(conn, w.selection).Reply()
	if err != nil {
		return nil, err
	}
	if owner.Owner != 0 {
		return nil, fmt.Errorf("another window manager owns this X screen")
	}
	mask := uint32(xproto.EventMaskSubstructureRedirect | xproto.EventMaskSubstructureNotify | xproto.EventMaskStructureNotify | xproto.EventMaskPropertyChange | xproto.EventMaskKeyPress)
	if err := xproto.ChangeWindowAttributesChecked(conn, w.root, xproto.CwEventMask, []uint32{mask}).Check(); err != nil {
		return nil, fmt.Errorf("claim X screen (another window manager may be running): %w", err)
	}
	w.owned = true
	if w.support, err = w.newWindow(0, 0, 1, 1, "gxw"); err != nil {
		return nil, err
	}
	if err := xproto.SetSelectionOwnerChecked(conn, w.support, w.selection, xproto.TimeCurrentTime).Check(); err != nil {
		return nil, err
	}
	owner, err = xproto.GetSelectionOwner(conn, w.selection).Reply()
	if err != nil || owner.Owner != w.support {
		return nil, fmt.Errorf("could not own WM selection")
	}
	w.setWords(w.root, "_NET_SUPPORTING_WM_CHECK", xproto.AtomWindow, uint32(w.support))
	w.setWords(w.support, "_NET_SUPPORTING_WM_CHECK", xproto.AtomWindow, uint32(w.support))
	var supported []uint32
	for _, name := range atomNames {
		if len(name) > 5 && name[:5] == "_NET_" {
			supported = append(supported, uint32(w.atoms[name]))
		}
	}
	w.setWords(w.root, "_NET_SUPPORTED", xproto.AtomAtom, supported...)
	w.setWords(w.root, "_NET_NUMBER_OF_DESKTOPS", xproto.AtomCardinal, 1)
	w.setWords(w.root, "_NET_CURRENT_DESKTOP", xproto.AtomCardinal, 0)
	w.setWords(w.root, "_NET_DESKTOP_VIEWPORT", xproto.AtomCardinal, 0, 0)
	w.setText(w.root, "_NET_DESKTOP_NAMES", "gxw\x00")
	e := xproto.ClientMessageEvent{Format: 32, Window: w.root, Type: w.atoms["MANAGER"], Data: xproto.ClientMessageDataUnionData32New([]uint32{0, uint32(w.selection), uint32(w.support), 0, 0})}
	xproto.SendEvent(conn, false, w.root, xproto.EventMaskStructureNotify, string(e.Bytes()))
	if w.tabbar, err = w.newWindow(0, 0, w.width, w.barHeight, "gxw tabs"); err != nil {
		return nil, err
	}
	if w.systembar, err = w.newWindow(0, w.height-w.systemStyle.Height(), w.width, w.systemStyle.Height(), "gxw system bar"); err != nil {
		return nil, err
	}
	w.gc, err = xproto.NewGcontextId(conn)
	if err != nil {
		return nil, err
	}
	if err = xproto.CreateGCChecked(conn, w.gc, xproto.Drawable(w.root), 0, nil).Check(); err != nil {
		return nil, err
	}
	for _, bar := range []xproto.Window{w.tabbar, w.systembar} {
		w.setWords(bar, "_NET_WM_WINDOW_TYPE", xproto.AtomAtom, uint32(w.atoms["_NET_WM_WINDOW_TYPE_DOCK"]))
		xproto.MapWindow(conn, bar)
	}
	w.updateWorkarea()
	if err := w.setupKeys(); err != nil {
		return nil, err
	}
	if err := w.startXIM(); err != nil {
		return nil, err
	}
	if randr.Init(conn) == nil {
		randr.SelectInput(conn, w.root, randr.NotifyMaskScreenChange)
	}
	tree, err := xproto.QueryTree(conn, w.root).Reply()
	if err != nil {
		return nil, err
	}
	for _, id := range tree.Children {
		a, err := xproto.GetWindowAttributes(conn, id).Reply()
		if err == nil && a.OverrideRedirect == false && a.Class == xproto.WindowClassInputOutput {
			state := w.words(id, "WM_STATE")
			if a.MapState != xproto.MapStateUnmapped || len(state) > 0 && state[0] == 3 {
				w.manage(id, false)
			}
		}
	}
	w.layout()
	ok = true
	return w, nil
}

func (w *wm) newWindow(x, y, width, height int, name string) (xproto.Window, error) {
	id, err := xproto.NewWindowId(w.conn)
	if err != nil {
		return 0, err
	}
	// Identity properties are queued before the first round trip.
	xproto.CreateWindow(w.conn, w.screen.RootDepth, id, w.root, int16(x), int16(y), uint16(width), uint16(height), 0, xproto.WindowClassInputOutput, w.screen.RootVisual,
		xproto.CwBackPixel|xproto.CwOverrideRedirect|xproto.CwEventMask, []uint32{w.pixels.pixel(45, 43, 70), 1, xproto.EventMaskExposure | xproto.EventMaskButtonPress})
	w.setText(id, "_NET_WM_NAME", name)
	xproto.ChangeProperty(w.conn, xproto.PropModeReplace, id, xproto.AtomWmClass, xproto.AtomString, 8, 8, []byte("gxw\x00Gxw\x00"))
	_, err = xproto.GetWindowAttributes(w.conn, id).Reply()
	return id, err
}

func (w *wm) workarea() image.Rectangle {
	top := min(w.barHeight, max(0, (w.height-1)/2))
	bottom := min(w.systemStyle.Height(), max(0, w.height-top-1))
	return image.Rect(0, top, w.width, w.height-bottom)
}

func (w *wm) updateWorkarea() {
	a := w.workarea()
	w.setWords(w.root, "_NET_WORKAREA", xproto.AtomCardinal, uint32(a.Min.X), uint32(a.Min.Y), uint32(a.Dx()), uint32(a.Dy()))
	w.setWords(w.root, "_NET_DESKTOP_GEOMETRY", xproto.AtomCardinal, uint32(w.width), uint32(w.height))
	if w.tabbar != 0 {
		xproto.ConfigureWindow(w.conn, w.tabbar, xproto.ConfigWindowWidth|xproto.ConfigWindowHeight, []uint32{uint32(w.width), uint32(a.Min.Y)})
		xproto.ConfigureWindow(w.conn, w.systembar, xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight, []uint32{uint32(a.Max.Y), uint32(w.width), uint32(w.height - a.Max.Y)})
		w.setWords(w.tabbar, "_NET_WM_STRUT_PARTIAL", xproto.AtomCardinal, 0, 0, uint32(a.Min.Y), 0, 0, 0, 0, 0, 0, uint32(w.width-1), 0, 0)
		w.setWords(w.systembar, "_NET_WM_STRUT_PARTIAL", xproto.AtomCardinal, 0, 0, 0, uint32(w.height-a.Max.Y), 0, 0, 0, 0, 0, 0, 0, uint32(w.width-1))
	}
}

func (w *wm) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type event struct {
		value xgb.Event
		err   xgb.Error
	}
	events := make(chan event, 64)
	conn := w.conn
	go func() {
		defer close(events)
		for {
			e, err := conn.WaitForEvent()
			if e == nil && err == nil {
				return
			}
			select {
			case events <- event{e, err}:
			case <-ctx.Done():
				return
			}
		}
	}()
	updates := make(chan systemStatus, 1)
	go systemWorker(ctx, w.jobs, updates)
	for !w.quit {
		select {
		case <-w.xim.pasteDeadline:
			w.xim.finishPaste()
			w.xim.drainCapture()
			w.notice("Input text is on the clipboard; application did not accept paste")
		case <-w.xim.wake:
			w.xim.poll()
		case <-ctx.Done():
			return nil
		case message := <-w.screenshots:
			w.screenshotBusy = false
			w.notice(message)
		case status := <-updates:
			w.status = status
			if status.err != "" {
				w.notice(status.err)
			}
			w.drawBars()
		case e, ok := <-events:
			if !ok {
				return fmt.Errorf("X connection closed")
			}
			if e.err != nil {
				// Clients can disappear between an event and our request.
				switch e.err.(type) {
				case xproto.WindowError, xproto.DrawableError, xproto.MatchError:
				default:
					log.Print(e.err)
				}
				continue
			}
			w.event(e.value)
		}
	}
	return nil
}

func (w *wm) event(event xgb.Event) {
	if w.xim != nil && w.xim.event(event) {
		return
	}
	switch e := event.(type) {
	case xproto.MapRequestEvent:
		if c := w.clients[e.Window]; c != nil {
			w.activate(c.id)
		} else {
			w.manage(e.Window, true)
		}
	case xproto.DestroyNotifyEvent:
		w.remove(e.Window)
	case xproto.UnmapNotifyEvent:
		if e.Event != w.root {
			return
		}
		if c := w.clients[e.Window]; c != nil {
			ours := slices.Contains(c.unmaps, e.Sequence)
			c.unmaps = slices.DeleteFunc(c.unmaps, func(seq uint16) bool { return int16(e.Sequence-seq) >= 0 })
			if !ours {
				// Honor a withdrawal even if it raced a queued tab remap.
				xproto.UnmapWindow(w.conn, c.id)
				w.remove(c.id)
			}
		}
	case xproto.ReparentNotifyEvent:
		if e.Parent != w.root {
			w.remove(e.Window)
		}
	case xproto.ConfigureRequestEvent:
		w.configureRequest(e)
	case xproto.ConfigureNotifyEvent:
		if e.Window == w.root {
			w.resize(int(e.Width), int(e.Height))
		}
	case randr.ScreenChangeNotifyEvent:
		if r, err := xproto.GetGeometry(w.conn, xproto.Drawable(w.root)).Reply(); err == nil {
			w.resize(int(r.Width), int(r.Height))
		}
	case xproto.PropertyNotifyEvent:
		if c := w.clients[e.Window]; c != nil {
			switch e.Atom {
			case w.atoms["WM_NAME"], w.atoms["_NET_WM_NAME"], w.atoms["WM_CLASS"]:
				w.readTitle(c)
				w.drawBars()
			case w.atoms["WM_NORMAL_HINTS"], w.atoms["WM_TRANSIENT_FOR"], w.atoms["_NET_WM_WINDOW_TYPE"], w.atoms["WM_HINTS"], w.atoms["WM_PROTOCOLS"]:
				w.readMetadata(c)
				w.layout()
			}
		}
	case xproto.ClientMessageEvent:
		w.clientMessage(e)
	case xproto.ExposeEvent:
		if e.Count == 0 && (e.Window == w.tabbar || e.Window == w.systembar) {
			w.drawBars()
		}
	case xproto.KeyPressEvent:
		w.timestamp = e.Time
		w.key(e)
	case xproto.MappingNotifyEvent:
		if err := w.setupKeys(); err != nil {
			w.notice(err.Error())
		}
	case xproto.ButtonPressEvent:
		w.timestamp = e.Time
		if e.Event == w.tabbar || e.Event == w.systembar {
			w.barClick(e)
		} else if c := w.clients[e.Event]; c != nil {
			w.focus(c.id)
			xproto.AllowEvents(w.conn, xproto.AllowReplayPointer, e.Time)
		}
	case xproto.SelectionClearEvent:
		if e.Selection == w.selection {
			w.quit = true
		}
	}
}

func (w *wm) resize(width, height int) {
	if width <= 0 || height <= 0 || width == w.width && height == w.height {
		return
	}
	w.width, w.height = width, height
	if err := w.updateScale(); err != nil {
		log.Printf("resize font: %v", err)
	}
	w.updateWorkarea()
	w.layout()
}

func resolutionScale(width, height int) int {
	if width > 1200 || height > 800 {
		return 2
	}
	return 1
}

func (w *wm) updateScale() error {
	systemScale := systembar.ResolutionScale(w.width, w.height)
	if w.systemStyle == nil || systemScale != w.systemScale {
		w.systemStyle = systembar.NewStyle(systemScale)
		w.systemScale = systemScale
	}
	scale := resolutionScale(w.width, w.height)
	if scale == w.scale {
		return nil
	}
	face, err := w.fonts.face(int(w.options.fontSize * float64(scale)))
	if err != nil {
		return err
	}
	previous := w.face
	w.face, w.scale = face, scale
	w.barHeight = face.Metrics().Height.Ceil()
	if previous != nil {
		previous.Close()
	}
	return nil
}

func (w *wm) notice(message string) {
	log.Print(message)
	w.message, w.messageUntil = message, time.Now().Add(8*time.Second)
	w.drawBars()
}

func (w *wm) close() {
	if w.conn == nil {
		return
	}
	if w.xim != nil {
		w.xim.close()
	}
	if w.owned {
		for _, id := range w.order {
			c := w.clients[id]
			r := c.original
			xproto.ConfigureWindow(w.conn, id, xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight|xproto.ConfigWindowBorderWidth, []uint32{uint32(r.Min.X), uint32(r.Min.Y), uint32(r.Dx()), uint32(r.Dy()), uint32(c.border)})
			xproto.DeleteProperty(w.conn, id, w.atoms["_NET_WM_STATE"])
			w.setWords(id, "WM_STATE", w.atoms["WM_STATE"], 1, 0)
			xproto.MapWindow(w.conn, id)
		}
		for _, name := range []string{"_NET_SUPPORTING_WM_CHECK", "_NET_SUPPORTED", "_NET_CLIENT_LIST", "_NET_CLIENT_LIST_STACKING", "_NET_ACTIVE_WINDOW", "_NET_WORKAREA", "_NET_NUMBER_OF_DESKTOPS", "_NET_CURRENT_DESKTOP", "_NET_DESKTOP_NAMES", "_NET_DESKTOP_VIEWPORT", "_NET_DESKTOP_GEOMETRY"} {
			xproto.DeleteProperty(w.conn, w.root, w.atoms[name])
		}
		xproto.SetInputFocus(w.conn, xproto.InputFocusPointerRoot, w.root, xproto.TimeCurrentTime)
		w.conn.Sync()
	}
	w.conn.Close()
	w.conn = nil
	if w.face != nil {
		w.face.Close()
	}
}
