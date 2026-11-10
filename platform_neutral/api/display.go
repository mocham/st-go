// Package api contains the backend-independent contracts used by the terminal
// host. Native event decoding stays in the backend; no native handles cross this
// boundary. This first split preserves the existing callbacks and pixel format.
package api

import "image"

type WindowOptions struct {
	Cols, Rows, CellWidth, CellHeight, Border int
	X, Y, GeometryMask                        int
	Title, Instance, Class                    string
	Fixed                                     bool
}

type Rect struct{ X, Y, Width, Height int }

// Callbacks are discrete host operations, not a universal native-event model.
// The backend calls them inside Lock/Unlock; timers use the same serialization
// domain. Text is committed UTF-8 (e.g. browser IME input), not clipboard paste;
// Paste is text already retrieved from the native clipboard.
type Callbacks struct {
	Lock, Unlock                 func()
	Closed                       func() bool
	Expose                       func()
	ExposeSurface                func(Surface)
	KeyPress                     func(key uint, modifiers uint16)
	Text                         func(string)
	PointerPress, PointerRelease func(button byte, modifiers uint16, x, y int)
	PointerMotion                func(modifiers uint16, x, y int)
	Resize                       func(width, height int)
	Focus                        func(bool)
	Close                        func()
	PaintDirty                   func()
	Paste                        func(string)
}

// EventHost is the uniform terminal-to-native callback API. Native event loops
// and their concrete event representations do not belong to the terminal core.
type EventHost interface {
	Bind(Callbacks)
	Run()
	SetupKeys()
	Show()
	ScreenSize() (width, height int)
	ClientSize() (width, height int, err error)
	WindowRect() (Rect, error)
	RequestWindowRect(Rect)
	SetTitle(string)
	SetIconTitle(string)
	SetClipboard(selection byte, text string)
	QueryClipboard(selection byte, reply func(string, bool))
	RequestPaste()
	Close()
}

// Painter consumes final opaque 0xAARRGGBB pixels. Upload copies the supplied
// pixels before returning, matching the existing terminal framebuffer lifetime.
type Painter interface {
	Upload(pixels []uint32, stride, x, y, width, height int)
	NewSurface(bounds image.Rectangle, background uint32) (Surface, error)
}

// Surface is an independently presented retained pane. Scroll reuse is optional;
// Present receives a complete frame so implementations can upload it in full.
// Calls are serialized by the host's existing presenter lock.
type Surface interface {
	Configure(bounds image.Rectangle, background uint32)
	Present(pixels []uint32, width, height, scrollRows int, reuse bool)
	Expose() bool
	Close()
}

type Host interface {
	EventHost
	Painter
}
type OpenHost func(WindowOptions) (Host, error)

// GeometrySupport lets hosts without desktop window placement ignore geometry
// requests without arming the terminal's click-to-restore interception.
type GeometrySupport interface{ SupportsWindowGeometry() bool }
