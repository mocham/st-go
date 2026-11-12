package frontend

import "sync"

// The executable supplies the embedded font at startup. Identity and the
// raster/cache lock are independent of the selected font engine.
var embeddedFont []byte
var fontMu sync.Mutex
var fontGeneration uint64

func invalidateGlyphsLocked() {
	fontGeneration++
	glyphMu.Lock()
	clear(glyphCache)
	glyphCacheBytes = 0
	glyphMu.Unlock()
}
