package frontend

/*
#cgo CFLAGS: -I${SRCDIR}/../../third_party/freetype/include -I${SRCDIR}/../..
#cgo LDFLAGS: ${SRCDIR}/../../third_party/freetype/libfreetype.a -lm -lpthread
#include <stdlib.h>
#include "third_party_wrapper/ff2.h"
#include "third_party_wrapper/plugin-ff2.c"
*/
import "C"

import (
	"log"
	"unsafe"

	"st-go/limits"
)

// The executable embeds the existing font asset and supplies it at startup.
var embeddedFont []byte

type ft2 struct {
	loaded bool
}

var ft ft2

// loadFonts initializes FreeType. When fontName is empty the embedded
// Monaco_Linux.ttf (go:embed) is loaded from memory; otherwise the named
// font file is used (the -f command-line override). Fallback slots are
// resolved by the C plugin as before.
func loadFonts(fontName string, pixsize int) bool {
	if _, valid := limits.RGBA("font size", pixsize, pixsize); !valid {
		return false
	}
	if fontName == "" {
		// The primary embedded font must outlive the library.
		ptr := unsafe.Pointer(&embeddedFont[0])
		C.ff2_set_memory_font(0, (*C.uchar)(ptr), C.size_t(len(embeddedFont)))
		C.ff2_set_memory_font(C.FF2_MAXFONTS-1, nil, 0)
	} else {
		C.ff2_set_memory_font(0, nil, 0)
		cname := C.CString(fontName)
		defer C.free(unsafe.Pointer(cname))
		C.ff2_set_font(0, cname)
		// Keep readable text even if a configured file is missing, corrupt or
		// only contains icons. This memory slot follows the primary file.
		C.ff2_set_memory_font(C.FF2_MAXFONTS-1, (*C.uchar)(unsafe.Pointer(&embeddedFont[0])), C.size_t(len(embeddedFont)))
	}
	ok := C.ff2_load_fonts(C.int(pixsize))
	ft.loaded = ok != 0
	glyphMu.Lock()
	clear(glyphCache)
	glyphCacheBytes = 0
	glyphMu.Unlock()
	if !ft.loaded {
		log.Printf("ft2: failed to load fonts\n")
	}
	return ft.loaded
}

// makeGlyph renders one rune blended over bg into dst (ARGB cells).
// Returns the number of columns used (GlyphWidth or 2*GlyphWidth).
func makeGlyph(r rune, fg, bg uint32, outW, outH, baseline int) (int, []uint32) {
	if outW < 1 || outH < 1 {
		return outW, nil
	}
	size, valid := limits.RGBA("glyph", outW, outH)
	if !valid {
		return outW, nil
	}
	dst := make([]uint32, size/4)
	if r < 0x20 || r == 0x7f || r == 0 {
		// control characters have no glyph; fill with background
		for i := range dst {
			dst[i] = bg
		}
		return outW, dst
	}
	// rune -> utf8 string (a rune's UTF-8 has no interior NUL)
	utf := string(r)
	cstr := C.CString(utf)
	defer C.free(unsafe.Pointer(cstr))
	used := int(C.make_ff2_glyph(
		cstr,
		C.uint32_t(fg),
		C.uint32_t(bg),
		C.int(outW),
		C.int(outH),
		C.int(baseline),
		(*C.uint32_t)(unsafe.Pointer(&dst[0])),
	))
	if used == 0 {
		// failure: fill with bg
		for i := range dst {
			dst[i] = bg
		}
		return outW, dst
	}
	return outW, dst
}
