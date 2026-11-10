package api

import "image"

// WidgetID is scoped to one native window. Zero is not a widget.
type WidgetID uint64

type WidgetKind uint8

const (
	TerminalWidget WidgetKind = iota
	BarWidget
)

type WidgetOptions struct {
	Kind               WidgetKind
	Bounds             image.Rectangle // window pixels; widget APIs use local pixels.
	AutoResize         bool            // follow the native client size.
	PointerTransparent bool            // clicks pass through this widget.
	Passive            bool            // reserve layout space for this bar; it may still handle input
	Hidden             bool            // start without presenting or receiving input.
}

// Widget is a scoped host: Close releases only this widget. The native window
// closes when its last terminal widget closes. There is no tab policy here.
type Widget interface {
	Host
	WidgetContainer
	ID() WidgetID
	Kind() WidgetKind
	Bounds() image.Rectangle
	SetBounds(image.Rectangle)
	Visible() bool
	SetVisible(bool)
	FocusWidget()
}

type WidgetContainer interface {
	NewWidget(WidgetOptions) (Widget, error)
}

// PositionedPainter separates source addressing from window placement. Legacy
// Upload addresses the source and destination with the same coordinates.
type PositionedPainter interface {
	UploadAt(pixels []uint32, stride int, source image.Rectangle, destination image.Point)
}

type SurfaceVisibility interface{ SetVisible(bool) }

// WidgetPresentation identifies a visibility/placement generation. Preview
// workers snapshot it before rendering so old work cannot paint after a hide,
// move, or reactivation, even if the widget is visible again by completion.
type WidgetPresentation interface {
	PresentationState() (generation uint64, visible bool)
}

type GuardedSurface interface {
	PresentIfCurrent(generation uint64, pixels []uint32, width, height, scrollRows int, reuse bool) bool
}

// WidgetSurfaceHost gives opaque bars their existing above-content ordering.
// Normal preview surfaces remain scoped to their terminal widget.
type WidgetSurfaceHost interface {
	NewWidgetSurface(WidgetKind, image.Rectangle, uint32) (Surface, error)
}

// WidgetAttacher registers backend-owned widgets after the window's routing
// layer exists. In particular, only framebuffer hosts register a system bar.
type WidgetAttacher interface{ AttachWidgets(WidgetContainer) }

// WindowHooks run in the native event domain, outside widget callback locks.
// Policies can consume window shortcuts and lay out widgets after native resize
// or a passive bar's layout changes.
type WindowHooks struct {
	Resize   func(width, height int)
	KeyPress func(key uint, modifiers uint16) bool
}

type WidgetWindow interface {
	WindowBounds() image.Rectangle
	BindWindowHooks(WindowHooks)
	SetAutoResize(bool)
	PostWindow(func()) bool
	RunWindow()
}

// WindowBarLayout exposes visible, pointer-transparent bar rectangles in window
// pixels. Optional layout policies can share their bottom band with native
// status widgets without depending on a backend or covering its controls.
type WindowBarLayout interface {
	PassiveBarBounds() []image.Rectangle
}

// WidgetSurfaceFactory avoids mapping even a blank surface for a background
// PTY, and preserves bar/content ordering from its first presentation.
type WidgetSurfaceFactory interface {
	NewWidgetSurfaceVisible(kind WidgetKind, bounds image.Rectangle, background uint32, visible bool) (Surface, error)
}
