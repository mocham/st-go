package api

import "image"

// Scene operations use surface-local device pixels. The terminal owns cell
// geometry; renderers must not reflow text using platform font measurements.
type TextRun struct {
	Cluster    bool   `json:"cluster,omitempty"` // Text is one cell sequence, not a rune run
	X          int    `json:"x"`
	Text       string `json:"text"`
	Advance    int    `json:"advance"`
	Foreground uint32 `json:"fg"`
	Background uint32 `json:"bg"`
	Style      uint16 `json:"style,omitempty"`
}

type SceneRow struct {
	X          int              `json:"x,omitempty"`
	Width      int              `json:"width,omitempty"` // zero means full surface width
	FontSize   int              `json:"fontSize,omitempty"`
	Y          int              `json:"y"`
	Height     int              `json:"height"`
	Baseline   int              `json:"baseline"`
	Background uint32           `json:"background"`
	Text       []TextRun        `json:"text,omitempty"`
	Media      []MediaPlacement `json:"media,omitempty"`
}

type SceneCursor struct {
	Text       string `json:"text,omitempty"`
	Foreground uint32 `json:"fg,omitempty"`
	Baseline   int    `json:"baseline,omitempty"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Shape      int    `json:"shape"`
	Thickness  int    `json:"thickness"`
	Color      uint32 `json:"color"`
	Outline    bool   `json:"outline,omitempty"`
}

// Updates replace complete horizontal bands. Apply must retain state before
// returning; presentation may coalesce updates but may never discard mutations.
type SceneUpdate struct {
	Fills      []SceneFill  `json:"fills,omitempty"`
	Reset      bool         `json:"reset,omitempty"`
	Background uint32       `json:"background,omitempty"`
	Rows       []SceneRow   `json:"rows,omitempty"`
	Cursor     *SceneCursor `json:"cursor,omitempty"`
}

type SceneFill struct {
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Color  uint32 `json:"color"`
}

type SceneTarget interface{ Apply(SceneUpdate) }
type SceneSurface interface {
	SceneTarget
	Configure(image.Rectangle, uint32)
	SetVisible(bool)
	Close()
}

type SceneFactory interface {
	NewSceneSurface(WidgetKind, image.Rectangle, uint32, bool) (SceneSurface, error)
	SetSceneFont([]byte)
}

type ScenePainter interface {
	Scene() SceneSurface
}

// Sources are immutable renderer resources. RegisterMedia schedules loading;
// it never performs file I/O or decoding on the terminal's event goroutine.
type MediaSource struct {
	Path string
	Data []byte
}
type MediaRegistry interface {
	RegisterMedia(MediaSource) uint64
	ReleaseMedia(uint64)
}
type MediaPlacement struct {
	ID         uint64 `json:"id"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	ClipX      int    `json:"clipX,omitempty"`
	ClipY      int    `json:"clipY,omitempty"`
	ClipWidth  int    `json:"clipWidth,omitempty"`
	ClipHeight int    `json:"clipHeight,omitempty"`
	Page       int    `json:"page,omitempty"`
	Fit        string `json:"fit,omitempty"`
}

// A preview is retained desired state. Generation changes cancel old work;
// completion is delivered asynchronously and must never block text painting.
type MediaView struct {
	Search     string `json:"search,omitempty"`
	ID         uint64 `json:"id"`
	Generation uint64 `json:"generation"`
	Page       int    `json:"page"`
	Fit        string `json:"fit"`
	Continuous bool   `json:"continuous,omitempty"`
	Background uint32 `json:"background"`
	Position   int    `json:"position"`
	PositionID uint64 `json:"positionID"`
	Scroll     int    `json:"scroll"`
	Zoom       int    `json:"zoom,omitempty"`
}
type MediaSurface interface{ PresentMedia(uint64, MediaView) bool }

// SemanticSurface uses the same widget generation guard as raster/HTML panes.
type SemanticSurface interface {
	PresentScene(uint64, SceneUpdate) bool
}

type DocumentQuery struct {
	Kind string `json:"kind"`
	Page int    `json:"page,omitempty"`
	Text string `json:"text,omitempty"`
}

// QueryMedia returns immediately. Completion carries a bounded JSON metadata
// payload and runs outside renderer locks. The cancellation function is idempotent.
type DocumentHost interface {
	QueryMedia(uint64, DocumentQuery, func([]byte, error)) func()
}

type MediaStatus struct {
	PositionID uint64 `json:"positionID"`
	ID         uint64 `json:"id"`
	Generation uint64 `json:"generation"`
	Page       int    `json:"page"`
	Pages      int    `json:"pages"`
	Position   int    `json:"position"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Error      string `json:"error,omitempty"`
}
type MediaObserver interface {
	WatchMedia(uint64, func(MediaStatus))
}
