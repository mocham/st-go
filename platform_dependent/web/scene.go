package web

import (
	"fmt"
	"image"
	"sort"

	"st-go/limits"
	"st-go/platform_neutral/api"
)

type sceneSurface struct {
	driver                       *Driver
	id                           uint64
	kind                         api.WidgetKind
	bounds                       image.Rectangle
	background                   uint32
	hidden, closed, dirty, reset bool
	rows                         map[int]api.SceneRow
	changed                      map[int]bool
	cursor                       api.SceneCursor
	fills                        []api.SceneFill
}
type sceneMessage struct {
	ID     uint64          `json:"id"`
	X      int             `json:"x"`
	Y      int             `json:"y"`
	Width  int             `json:"width"`
	Height int             `json:"height"`
	Bar    bool            `json:"bar,omitempty"`
	Hidden bool            `json:"hidden,omitempty"`
	Update api.SceneUpdate `json:"update"`
}

func (d *Driver) SetSceneFont(data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.font) == 0 {
		d.font = append([]byte(nil), data...)
	}
}
func (d *Driver) NewSceneSurface(kind api.WidgetKind, bounds image.Rectangle, bg uint32, visible bool) (api.SceneSurface, error) {
	if _, ok := limits.RGBA("scene", bounds.Dx(), bounds.Dy()); !ok {
		return nil, fmt.Errorf("invalid scene size")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextSurfaceID++
	s := &sceneSurface{driver: d, id: d.nextSurfaceID, kind: kind, bounds: bounds, background: bg, hidden: !visible, dirty: true, reset: true, rows: make(map[int]api.SceneRow), changed: make(map[int]bool)}
	d.scenes = append(d.scenes, s)
	// Scene surfaces retain semantic state; no terminal-sized server raster.
	d.pixels, d.dirty = nil, nil
	d.notify()
	return s, nil
}
func (s *sceneSurface) Apply(update api.SceneUpdate) {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	s.apply(update)
}
func (s *sceneSurface) apply(update api.SceneUpdate) {
	d := s.driver
	if s.closed {
		return
	}
	if update.Reset {
		clear(s.rows)
		clear(s.changed)
		s.background = update.Background
		s.reset = true
	}
	if update.Reset || update.Fills != nil {
		s.fills = append(s.fills[:0], update.Fills...)
	}
	for _, row := range update.Rows {
		if row.Height < 1 || row.Y < 0 || row.Y >= s.bounds.Dy() {
			continue
		}
		row.Text = append([]api.TextRun(nil), row.Text...)
		row.Media = append([]api.MediaPlacement(nil), row.Media...)
		s.rows[row.Y] = row
		s.changed[row.Y] = true
	}
	if update.Cursor != nil {
		s.cursor = *update.Cursor
	}
	s.dirty = true
	d.notify()
}

func (s *surface) PresentScene(_ uint64, update api.SceneUpdate) bool {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed || s.hidden {
		return false
	}
	if s.scene == nil {
		d.nextSurfaceID++
		s.scene = &sceneSurface{driver: d, id: d.nextSurfaceID, bounds: s.bounds, rows: make(map[int]api.SceneRow), changed: make(map[int]bool), reset: true}
		d.scenes = append(d.scenes, s.scene)
	}
	s.scene.hidden = false
	s.scene.apply(update)
	s.html = api.HTMLView{}
	s.media = api.MediaView{}
	s.pixels = nil
	d.htmlVersion++
	d.notify()
	return true
}
func (s *sceneSurface) Configure(bounds image.Rectangle, bg uint32) {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed || s.bounds == bounds {
		return
	}
	for y, row := range s.rows {
		if y+row.Height > bounds.Dy() {
			delete(s.rows, y)
		}
	}
	s.bounds = bounds
	s.background = bg
	s.reset = true
	s.dirty = true
	d.notify()
}
func (s *sceneSurface) SetVisible(visible bool) {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed || s.hidden == !visible {
		return
	}
	s.hidden = !visible
	s.reset = true
	s.dirty = true
	d.notify()
}
func (s *sceneSurface) Close() {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	s.closed = true
	s.hidden = true
	s.dirty = true
	s.rows = nil
	d.notify()
}
func (d *Driver) sceneSnapshot(full bool) *serverMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := &serverMessage{Type: "scene", Width: d.width, Height: d.height}
	for _, s := range d.scenes {
		if !s.dirty && !full {
			continue
		}
		u := api.SceneUpdate{Reset: s.reset || full, Background: s.background, Cursor: &api.SceneCursor{}}
		u.Fills = append([]api.SceneFill(nil), s.fills...)
		*u.Cursor = s.cursor
		if !s.hidden {
			for y, row := range s.rows {
				if u.Reset || s.changed[y] {
					u.Rows = append(u.Rows, row)
				}
			}
			sort.Slice(u.Rows, func(i, j int) bool { return u.Rows[i].Y < u.Rows[j].Y })
		}
		m.Scenes = append(m.Scenes, sceneMessage{ID: s.id, X: s.bounds.Min.X, Y: s.bounds.Min.Y, Width: s.bounds.Dx(), Height: s.bounds.Dy(), Bar: s.kind == api.BarWidget, Hidden: s.hidden, Update: u})
		s.dirty = false
		s.reset = false
		clear(s.changed)
	}
	live := d.scenes[:0]
	for _, s := range d.scenes {
		if !s.closed {
			live = append(live, s)
		}
	}
	d.scenes = live
	if len(m.Scenes) == 0 {
		return nil
	}
	return m
}
