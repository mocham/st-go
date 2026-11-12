//go:build !backend_web && cgo && !purego

package frontend

/*
#cgo CFLAGS: -I${SRCDIR}/../../third_party/freetype/include -I${SRCDIR}/../..
#cgo LDFLAGS: ${SRCDIR}/../../third_party/freetype/libfreetype.a -lm -lpthread
#include "third_party_wrapper/font_freetype.c"
*/
import "C"

import (
	"fmt"
	"image"
	"runtime"
	"unsafe"

	"st-go/limits"
	"st-go/platform_neutral/fontraster"
)

type nativeFontFace struct{ face *C.st_font_face }

func openFontFace(data []byte, pixelHeight int) (fontraster.Face, error) {
	if len(data) == 0 || !limits.Check("font input", int64(len(data)), limits.FileBytes) {
		return nil, fmt.Errorf("invalid font input size")
	}
	if _, ok := limits.RGBA("font size", pixelHeight, pixelHeight); !ok {
		return nil, fmt.Errorf("invalid font size")
	}
	// C copies these bytes during the call; it never retains a Go pointer.
	face := C.st_font_open((*C.uchar)(unsafe.Pointer(&data[0])), C.size_t(len(data)), C.int(pixelHeight))
	runtime.KeepAlive(data)
	if face == nil {
		return nil, fmt.Errorf("FreeType could not load font")
	}
	return &nativeFontFace{face: face}, nil
}

func (f *nativeFontFace) Close() error {
	C.st_font_close(f.face)
	f.face = nil
	return nil
}

func (f *nativeFontFace) Glyph(r rune) (fontraster.Glyph, bool, error) {
	var width, height, top C.int
	switch C.st_font_glyph(f.face, C.uint32_t(r), C.int(fontraster.MaskCacheBytes), &width, &height, &top) {
	case 0:
		return fontraster.Glyph{}, false, nil
	case 1:
	default:
		return fontraster.Glyph{}, false, fmt.Errorf("FreeType could not rasterize %U", r)
	}
	if width == 0 || height == 0 {
		return fontraster.Glyph{}, true, nil
	}
	if _, ok := limits.Product("glyph mask", fontraster.MaskCacheBytes, int(width), int(height)); !ok {
		return fontraster.Glyph{}, false, fmt.Errorf("glyph bounds exceed bitmap limit")
	}
	mask := image.NewAlpha(image.Rect(0, 0, int(width), int(height)))
	if C.st_font_copy_mask(f.face, (*C.uchar)(unsafe.Pointer(&mask.Pix[0])), C.size_t(len(mask.Pix))) != 1 {
		return fontraster.Glyph{}, false, fmt.Errorf("invalid FreeType bitmap")
	}
	return fontraster.Glyph{Mask: mask, Top: int(top)}, true, nil
}
