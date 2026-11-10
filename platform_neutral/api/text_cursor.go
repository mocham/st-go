package api

import (
	"image"
	"slices"
)

// TextCursor is a small, opaque, precomposited terminal caret, not a mouse
// pointer. Empty bounds hide it. Pixels are row-major and local to Bounds.
// The glyph/content framebuffer never contains these pixels.
type TextCursor struct {
	Bounds image.Rectangle
	Pixels []uint32
	// Base is the clean cell underneath, allowing a non-compositing host to
	// restore an old caret without retaining a second full-screen framebuffer.
	Base []uint32
}

func (c TextCursor) Equal(other TextCursor) bool {
	return c.Bounds == other.Bounds && slices.Equal(c.Pixels, other.Pixels) && slices.Equal(c.Base, other.Base)
}

func (c TextCursor) Valid() bool {
	w, h := c.Bounds.Dx(), c.Bounds.Dy()
	return !c.Bounds.Empty() && w > 0 && h > 0 && len(c.Pixels)/w == h && len(c.Pixels)%w == 0 && len(c.Base) == len(c.Pixels)
}

// TextCursorPainter keeps caret damage independent from content uploads.
// Implementations copy the pixels before returning. Mouse pointers remain
// backend-owned (native on X11/web, software on framebuffer).
type TextCursorPainter interface {
	SetTextCursor(TextCursor)
}
