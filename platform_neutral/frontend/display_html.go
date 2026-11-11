package frontend

import (
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

func (t *Terminal) SupportsHTML() bool {
	host, ok := t.host.(api.HTMLSupport)
	return ok && host.SupportsHTML()
}

func (t *Terminal) SetRegionHTML(id term.RegionID, document api.HTMLDocument) error {
	if !t.SupportsHTML() {
		return nil // fb/X11 reserve the panel, but discard browser content.
	}
	s := &t.displayRegions
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := t.clearRegion(id); err != nil {
		return err
	}
	s.secondary.html = api.HTMLView{Document: &document}
	s.secondary.dirty = true
	t.requestRegionPaint()
	return nil
}
