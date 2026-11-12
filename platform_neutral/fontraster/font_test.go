package fontraster

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

type testFace struct {
	glyphs map[rune]Glyph
	calls  int
	closed bool
}

func (f *testFace) Glyph(r rune) (Glyph, bool, error) {
	f.calls++
	g, ok := f.glyphs[r]
	return g, ok, nil
}
func (f *testFace) Close() error { f.closed = true; return nil }

func maskGlyph(width, height, top int, alpha byte) Glyph {
	m := image.NewAlpha(image.Rect(0, 0, width, height))
	for i := range m.Pix {
		m.Pix[i] = alpha
	}
	return Glyph{Mask: m, Top: top}
}

func TestFallbackMissingAndEmptyGlyphs(t *testing.T) {
	primary := &testFace{glyphs: map[rune]Glyph{' ': {}, 'A': maskGlyph(1, 1, 1, 255)}}
	fallback := &testFace{glyphs: map[rune]Glyph{' ': maskGlyph(1, 1, 1, 255), '中': maskGlyph(2, 1, 1, 128)}}
	s := &Set{faces: []Face{primary, fallback}, cache: make(map[rune]Glyph)}
	if g := s.Glyph(' '); g.Mask != nil || fallback.calls != 0 {
		t.Fatal("empty primary glyph fell through to another font")
	}
	if g := s.Glyph('中'); g.Mask == nil || g.Mask.Rect.Dx() != 2 {
		t.Fatal("missing character did not reach fallback")
	}
	for i := 0; i < 10; i++ {
		Compose(s.Glyph('中'), uint32(i), 0, 4, 3, 2)
	}
	if fallback.calls != 1 {
		t.Fatal("changing colors rasterized the mask again")
	}
	if g := s.Glyph('\U0010FFFF'); g.Mask != nil {
		t.Fatal("missing character unexpectedly has ink")
	}
	calls := primary.calls + fallback.calls
	s.Glyph('\U0010FFFF')
	s.Glyph('\n')
	if primary.calls+fallback.calls != calls {
		t.Fatal("missing/control glyph not cached/suppressed")
	}
	s.Close()
	s.Close()
	if !primary.closed || !fallback.closed || len(s.cache) != 0 || s.bytes != 0 {
		t.Fatal("font resources retained after close")
	}
}

func TestComposePlacementClippingAndColors(t *testing.T) {
	const fg, bg = uint32(0xff204060), uint32(0xffe0c0a0)
	for _, test := range []struct {
		name           string
		glyph          Glyph
		w, h, baseline int
		ink            image.Rectangle
	}{
		{"center", maskGlyph(2, 2, 2, 255), 6, 5, 3, image.Rect(2, 1, 4, 3)},
		{"wide cell", maskGlyph(2, 2, 2, 255), 12, 5, 3, image.Rect(5, 1, 7, 3)},
		{"clip above", maskGlyph(2, 4, 4, 255), 4, 3, 2, image.Rect(1, 0, 3, 2)},
		{"clip right bottom", maskGlyph(8, 5, 1, 255), 4, 3, 2, image.Rect(0, 1, 4, 3)},
		{"offscreen", maskGlyph(2, 2, 8, 255), 4, 3, 2, image.Rectangle{}},
		{"empty", Glyph{}, 4, 3, 2, image.Rectangle{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pixels := Compose(test.glyph, fg, bg, test.w, test.h, test.baseline)
			for y := 0; y < test.h; y++ {
				for x := 0; x < test.w; x++ {
					want := bg
					if image.Pt(x, y).In(test.ink) {
						want = fg
					}
					if got := pixels[y*test.w+x]; got != want {
						t.Fatalf("pixel %d,%d = %x, want %x", x, y, got, want)
					}
				}
			}
		})
	}
	if Blend(0, fg, bg) != bg || Blend(255, fg, bg) != fg || Blend(128, 0xff000000, 0xffffffff) != 0xff7f7f7f {
		t.Fatal("incorrect grayscale blending")
	}
	if Compose(Glyph{}, fg, bg, 0, 3, 2) != nil {
		t.Fatal("invalid geometry accepted")
	}
}

func TestMaskCacheBoundsAndConcurrentUse(t *testing.T) {
	f := &testFace{glyphs: map[rune]Glyph{'A': maskGlyph(1, 1, 1, 255)}}
	s := &Set{faces: []Face{f}, cache: make(map[rune]Glyph)}
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < MaskCacheEntries*2; i++ {
				s.Glyph(rune(i + 32))
			}
		}()
	}
	workers.Wait()
	if len(s.cache) > MaskCacheEntries || s.bytes > MaskCacheBytes {
		t.Fatal("unbounded mask cache")
	}
	// Force a byte-budget eviction as well as the zero-size entry-budget case.
	clear(s.cache)
	s.bytes = 0
	large := maskGlyph(1024, 1024, 1, 255)
	for r := rune('a'); r <= 'z'; r++ {
		f.glyphs[r] = large
	}
	for r := rune('a'); r <= 'z'; r++ {
		s.Glyph(r)
	}
	if s.bytes == 0 || s.bytes > MaskCacheBytes {
		t.Fatal("mask byte budget exceeded")
	}
}

func TestLoadOrderAndFailureIsolation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	fontDir := filepath.Join(dir, ".local/share/fonts")
	if err := os.MkdirAll(fontDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"custom.ttf", "fontawesome-solid.ttf", "msyh.ttc"} {
		if err := os.WriteFile(filepath.Join(fontDir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var loaded []string
	loader := func(data []byte, size int) (Face, error) {
		loaded = append(loaded, string(data))
		if size != 28 {
			t.Fatalf("wrong raster size %d", size)
		}
		if string(data) == "custom.ttf" {
			return nil, fmt.Errorf("bad primary")
		}
		return &testFace{}, nil
	}
	s, err := Load(loader, "custom.ttf", []byte("embedded"), 28)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if fmt.Sprint(loaded) != "[custom.ttf embedded fontawesome-solid.ttf msyh.ttc]" || len(s.faces) != 3 {
		t.Fatalf("load order/fallback = %v (%d faces)", loaded, len(s.faces))
	}
}

func trueTypeCollection(data []byte) []byte {
	// A one-face TTC uses absolute table offsets into the entire collection.
	collection := make([]byte, 16+len(data))
	copy(collection, "ttcf")
	binary.BigEndian.PutUint32(collection[4:], 0x10000)
	binary.BigEndian.PutUint32(collection[8:], 1)
	binary.BigEndian.PutUint32(collection[12:], 16)
	copy(collection[16:], data)
	for i := 0; i < int(binary.BigEndian.Uint16(data[4:])); i++ {
		at := 16 + 12 + i*16 + 8
		binary.BigEndian.PutUint32(collection[at:], binary.BigEndian.Uint32(collection[at:])+16)
	}
	return collection
}

func TestGoFaceCoverageOwnershipAndTTC(t *testing.T) {
	for _, data := range [][]byte{goregular.TTF, trueTypeCollection(goregular.TTF)} {
		face, err := OpenGo(data, 28)
		if err != nil {
			t.Fatal(err)
		}
		defer face.Close()
		first, found, err := face.Glyph('A')
		if err != nil || !found || first.Mask == nil {
			t.Fatalf("A: %v, %v", found, err)
		}
		saved := bytes.Clone(first.Mask.Pix)
		for _, r := range "Hello, world!0123456789" {
			g, found, err := face.Glyph(r)
			if err != nil || !found {
				t.Fatalf("%q: %v, %v", r, found, err)
			}
			if r != ' ' && (g.Mask == nil || bytes.Count(g.Mask.Pix, []byte{0}) == len(g.Mask.Pix)) {
				t.Fatalf("blank %q", r)
			}
		}
		if !bytes.Equal(saved, first.Mask.Pix) {
			t.Fatal("glyph aliases mutable face storage")
		}
		if _, found, err := face.Glyph('\U0010FFFF'); err != nil || found {
			t.Fatalf(".notdef reported as coverage: %v, %v", found, err)
		}
		face.Close()
		if _, found, err := face.Glyph('A'); err != nil || found {
			t.Fatal("closed face retained data")
		}
	}
	for _, data := range [][]byte{nil, []byte("not a font"), goregular.TTF[:100]} {
		if face, err := OpenGo(data, 28); err == nil {
			face.Close()
			t.Fatal("malformed font accepted")
		}
	}
}
