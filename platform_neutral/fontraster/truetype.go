package fontraster

import (
	"fmt"
	"image"

	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"st-go/limits"
)

type goFace struct {
	font *truetype.Font
	face font.Face
}

// OpenGo opens TrueType outlines, including the first face of a TTC. The modern
// SFNT reader validates table/cmap ranges before the older FreeType-Go parser.
func OpenGo(data []byte, pixelHeight int) (result Face, err error) {
	defer func() {
		if problem := recover(); problem != nil {
			result, err = nil, fmt.Errorf("invalid TrueType font: %v", problem)
		}
	}()
	if !limits.Check("font input", int64(len(data)), limits.FileBytes) {
		return nil, fmt.Errorf("font exceeds input limit")
	}
	if _, ok := limits.RGBA("font size", pixelHeight, pixelHeight); !ok {
		return nil, fmt.Errorf("invalid font size")
	}
	collection, err := sfnt.ParseCollection(data)
	if err != nil {
		return nil, err
	}
	if _, err := collection.Font(0); err != nil {
		return nil, err
	}
	parsed, err := truetype.Parse(data)
	if err != nil {
		return nil, err
	}
	if parsed.FUnitsPerEm() <= 0 {
		return nil, fmt.Errorf("invalid font units per em")
	}
	// NewFace allocates an atlas from the font-wide bounds. Check it before
	// allocation, even though individual cell images are checked separately.
	b := parsed.Bounds(fixed.I(pixelHeight))
	w := b.Max.X.Ceil() - b.Min.X.Floor()
	h := b.Max.Y.Ceil() - b.Min.Y.Floor()
	if _, ok := limits.Product("font scratch mask", MaskCacheBytes, w, h); !ok {
		return nil, fmt.Errorf("font bounds exceed bitmap limit")
	}
	return &goFace{font: parsed, face: truetype.NewFace(parsed, &truetype.Options{
		Size: float64(pixelHeight), DPI: 72, Hinting: font.HintingFull,
		GlyphCacheEntries: 1, SubPixelsX: 1, SubPixelsY: 1,
	})}, nil
}

func (f *goFace) Close() error {
	if f.face == nil {
		return nil
	}
	err := f.face.Close()
	f.face, f.font = nil, nil
	return err
}

func (f *goFace) Glyph(r rune) (glyph Glyph, found bool, err error) {
	defer func() {
		if problem := recover(); problem != nil {
			glyph, found, err = Glyph{}, false, fmt.Errorf("invalid glyph %U: %v", r, problem)
		}
	}()
	// font.Face.Glyph can successfully render .notdef. That must not stop
	// lookup in the embedded/icon/CJK fallback faces.
	if f.font == nil || f.font.Index(r) == 0 {
		return Glyph{}, false, nil
	}
	rect, mask, origin, _, ok := f.face.Glyph(fixed.Point26_6{}, r)
	if !ok {
		return Glyph{}, false, fmt.Errorf("cannot rasterize %U", r)
	}
	if rect.Empty() {
		return Glyph{}, true, nil
	}
	if _, ok := limits.Product("glyph mask", MaskCacheBytes, rect.Dx(), rect.Dy()); !ok {
		return Glyph{}, false, fmt.Errorf("glyph bounds exceed bitmap limit")
	}
	owned := image.NewAlpha(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	// The face reuses its atlas on the next Glyph call. Copy only this glyph,
	// including its actual mask origin rather than the atlas's first row.
	alpha := mask.(*image.Alpha)
	for y := 0; y < rect.Dy(); y++ {
		at := alpha.PixOffset(origin.X, origin.Y+y)
		copy(owned.Pix[y*owned.Stride:], alpha.Pix[at:at+rect.Dx()])
	}
	return Glyph{Mask: owned, Top: -rect.Min.Y}, true, nil
}
