package widgets

import (
	"fmt"
	"st-go/platform_neutral/api"
)

func (h *Host) Scene() api.SceneSurface { return h.scene }
func (h *Host) SetSceneFont(data []byte) {
	if host, ok := h.window.native.(api.SceneFactory); ok {
		host.SetSceneFont(data)
	}
}
func (h *Host) RegisterMedia(source api.MediaSource) uint64 {
	if host, ok := h.window.native.(api.MediaRegistry); ok {
		return host.RegisterMedia(source)
	}
	return 0
}
func (h *Host) ReleaseMedia(id uint64) {
	if host, ok := h.window.native.(api.MediaRegistry); ok {
		host.ReleaseMedia(id)
	}
}
func (h *Host) WatchMedia(id uint64, done func(api.MediaStatus)) {
	if host, ok := h.window.native.(api.MediaObserver); ok {
		host.WatchMedia(id, done)
	}
}
func (h *Host) QueryMedia(id uint64, query api.DocumentQuery, done func([]byte, error)) func() {
	if host, ok := h.window.native.(api.DocumentHost); ok {
		return host.QueryMedia(id, query, done)
	}
	go done(nil, fmt.Errorf("document renderer unavailable"))
	return func() {}
}
func (s *surface) PresentMedia(generation uint64, view api.MediaView) bool {
	w := s.owner.window
	w.paintMu.Lock()
	defer w.paintMu.Unlock()
	current, visible := s.owner.PresentationState()
	if s.closed || s.hidden || !visible || current != generation {
		return false
	}
	native, ok := s.native.(api.MediaSurface)
	if !ok {
		return false
	}
	s.committed = Buffer{}
	return native.PresentMedia(0, view)
}

func (s *surface) PresentScene(generation uint64, update api.SceneUpdate) bool {
	w := s.owner.window
	w.paintMu.Lock()
	defer w.paintMu.Unlock()
	current, visible := s.owner.PresentationState()
	if s.closed || s.hidden || !visible || current != generation {
		return false
	}
	native, ok := s.native.(api.SemanticSurface)
	if !ok {
		return false
	}
	s.committed = Buffer{}
	return native.PresentScene(0, update)
}
