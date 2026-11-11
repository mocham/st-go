package web

import "st-go/platform_neutral/api"

type htmlOverlay struct {
	ID     uint64       `json:"id"`
	X      int          `json:"x"`
	Y      int          `json:"y"`
	Width  int          `json:"width"`
	Height int          `json:"height"`
	View   api.HTMLView `json:"view"`
}

func (*Driver) SupportsHTML() bool { return true }

func (s *surface) PresentHTML(_ uint64, view api.HTMLView) bool {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed || s.hidden {
		return false
	}
	if s.html != view {
		s.html = view
		d.htmlVersion++
		d.notify()
	}
	return true
}

// Snapshot all visible panes. Widget surfaces already own geometry, tab
// visibility, close and resize, so HTML follows the same lifecycle as pixels.
func (d *Driver) htmlSnapshot(previous uint64) (uint64, *serverMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if previous == d.htmlVersion {
		return previous, nil
	}
	m := &serverMessage{Type: "html", Width: d.width, Height: d.height, Overlays: []htmlOverlay{}}
	for _, s := range d.surfaces {
		if !s.closed && !s.hidden && s.html.Document != nil {
			m.Overlays = append(m.Overlays, htmlOverlay{s.id, s.bounds.Min.X, s.bounds.Min.Y, s.bounds.Dx(), s.bounds.Dy(), s.html})
		}
	}
	return d.htmlVersion, m
}
