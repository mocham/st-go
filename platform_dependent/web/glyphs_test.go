package web

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/draw"
	"image/png"
	"testing"

	"st-go/platform_neutral/api"
)

// A protocol consumer independent of the encoder. Compare the reconstructed
// canvas, including overlays and partial damage, with the original raster.
type glyphViewer struct {
	pixels                           *image.RGBA
	glyphs                           map[uint32]image.Image
	definitions, draws, resets, pngs int
	atlases                          int
}

func (v *glyphViewer) receive(t *testing.T, messages [][]byte) {
	t.Helper()
	if v.glyphs == nil {
		v.glyphs = make(map[uint32]image.Image)
	}
	for _, data := range messages {
		if len(data) < 12 {
			t.Fatal("short paint message")
		}
		offset := 4
		word := func() uint32 {
			if offset+4 > len(data) {
				t.Fatal("truncated paint message")
			}
			n := binary.LittleEndian.Uint32(data[offset:])
			offset += 4
			return n
		}
		w, h := int(word()), int(word())
		if v.pixels == nil || v.pixels.Bounds().Dx() != w || v.pixels.Bounds().Dy() != h {
			v.pixels = image.NewRGBA(image.Rect(0, 0, w, h))
		}
		decode := func(length int) image.Image {
			if length <= 0 || offset+length > len(data) {
				t.Fatal("invalid PNG length")
			}
			img, err := png.Decode(bytes.NewReader(data[offset : offset+length]))
			if err != nil {
				t.Fatal(err)
			}
			offset += length
			return img
		}
		switch string(data[:4]) {
		case "STW1":
			x, y, pw, ph := int(word()), int(word()), int(word()), int(word())
			img := decode(len(data) - offset)
			if img.Bounds().Size() != image.Pt(pw, ph) {
				t.Fatal("incorrect PNG dimensions")
			}
			draw.Draw(v.pixels, image.Rect(x, y, x+pw, y+ph), img, image.Point{}, draw.Src)
			v.pngs++
		case "STG1":
			count := word()
			for range count {
				switch word() {
				case 0:
					clear(v.glyphs)
					v.resets++
				case 3:
					id, count, gw, gh, cols, length := word(), int(word()), int(word()), int(word()), int(word()), int(word())
					img := decode(length)
					if img.Bounds().Size() != image.Pt(gw*cols, gh*((count+cols-1)/cols)) {
						t.Fatal("incorrect atlas dimensions")
					}
					for n := range count {
						if id == 0 || v.glyphs[id] != nil {
							t.Fatal("duplicate/invalid definition")
						}
						glyph := image.NewRGBA(image.Rect(0, 0, gw, gh))
						draw.Draw(glyph, glyph.Bounds(), img, image.Pt(n%cols*gw, n/cols*gh), draw.Src)
						v.glyphs[id] = glyph
						id++
					}
					v.definitions += count
					v.atlases++
				case 2:
					id, x, y, count := word(), int(word()), int(word()), int(word())
					img := v.glyphs[id]
					if img == nil || count < 1 {
						t.Fatal("draw references missing glyph or empty run")
					}
					for range count {
						draw.Draw(v.pixels, img.Bounds().Add(image.Pt(x, y)), img, image.Point{}, draw.Src)
						x += img.Bounds().Dx()
						v.draws++
					}
				default:
					t.Fatal("unknown glyph operation")
				}
			}
		default:
			t.Fatal("unknown paint message")
		}
		if offset != len(data) {
			t.Fatal("unconsumed protocol bytes")
		}
	}
}

func (v *glyphViewer) check(t *testing.T, f frame) {
	t.Helper()
	for y := f.rect.Min.Y; y < f.rect.Max.Y; y++ {
		for x := f.rect.Min.X; x < f.rect.Max.X; x++ {
			c := v.pixels.RGBAAt(x, y)
			got := uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
			want := f.pixels[(y-f.rect.Min.Y)*f.rect.Dx()+x-f.rect.Min.X] & 0xffffff
			if got != want || c.A != 255 {
				t.Fatalf("pixel (%d,%d) = %#08x alpha %d, want %#08x", x, y, got, c.A, want)
			}
		}
	}
}

func encodeGlyphTest(t *testing.T, e *glyphEncoder, v *glyphViewer, f frame) [][]byte {
	t.Helper()
	messages, err := e.encode(f)
	if err != nil {
		t.Fatal(err)
	}
	v.receive(t, messages)
	v.check(t, f)
	return messages
}

func TestGlyphCacheWarmDamageUsesOnlyIDsAndRuns(t *testing.T) {
	e := newGlyphEncoder(8, 16, 1)
	f := frame{82, 18, image.Rect(1, 1, 81, 17), make([]uint32, 80*16)}
	for y := 0; y < 16; y++ {
		for x := 0; x < 80; x++ {
			f.pixels[y*80+x] = 0xff000000 | uint32((x%8)*25)<<16 | uint32(y*12)
		}
	}
	v := new(glyphViewer)
	encodeGlyphTest(t, e, v, f)
	definitions, pngs := v.definitions, v.pngs
	if definitions != 1 {
		t.Fatalf("defined the same glyph %d times", definitions)
	}
	messages := encodeGlyphTest(t, e, v, f)
	if len(messages) != 1 || string(messages[0][:4]) != "STG1" || len(messages[0]) != 16+20 {
		t.Fatal("warm repeated glyphs did not collapse to one ID run")
	}
	if v.definitions != definitions || v.pngs != pngs {
		t.Fatal("warm glyph damage retransmitted pixels")
	}
	// The same pixels at a new position/viewport reuse IDs, including after
	// resize. A fresh connection must instead rebuild its own dictionary.
	f.width, f.height = 90, 40
	f.rect = f.rect.Add(image.Pt(8, 16))
	encodeGlyphTest(t, e, v, f)
	if v.definitions != definitions || v.pngs != pngs {
		t.Fatal("position or viewport size unnecessarily invalidated glyphs")
	}
	encodeGlyphTest(t, newGlyphEncoder(8, 16, 1), new(glyphViewer), f)
}

func TestGlyphCacheMatchesCompositedAndPartialDamage(t *testing.T) {
	d := testDriver(t)
	e, v := newGlyphEncoder(2, 2, 0), new(glyphViewer)
	pixels := make([]uint32, d.width*d.height)
	for i := range pixels {
		pixels[i] = 0xff112233 + uint32(i%2)*0x112233
	}
	check := func() {
		for _, f := range d.frames() {
			encodeGlyphTest(t, e, v, f)
		}
	}
	d.Upload(pixels, d.width, 0, 0, d.width, d.height)
	check()
	view, err := d.NewSurface(image.Rect(3, 1, 7, 5), 0xffabcdef)
	if err != nil {
		t.Fatal(err)
	}
	check()
	// An overlay crossing cell boundaries must never match the underlying
	// glyph. Colors/font changes are likewise distinct raster identities.
	for i := range pixels {
		pixels[i] ^= 0x00123456
	}
	d.Upload(pixels, d.width, 0, 0, d.width, d.height)
	check()
	view.Close()
	check()
	d.SetTextCursor(api.TextCursor{Bounds: image.Rect(4, 2, 6, 4), Pixels: []uint32{0xffaabbcc, 0xff112233, 0xff334455, 0xff667788}, Base: make([]uint32, 4)})
	check()
	d.SetTextCursor(api.TextCursor{})
	check()
	d.Upload(pixels, d.width, 1, 1, 3, 3)
	check()
	if v.draws == 0 || v.pngs == 0 {
		t.Fatal("test did not exercise both glyph and PNG paths")
	}
}

func TestGlyphCacheBudgetsResetInWireOrder(t *testing.T) {
	for _, budget := range []string{"bytes", "entries"} {
		t.Run(budget, func(t *testing.T) {
			e := newGlyphEncoder(2, 2, 0)
			if budget == "bytes" {
				e.maxBytes = 2 * 2 * 2 * 4
			} else {
				e.maxEntries = 2
			}
			f := frame{16, 4, image.Rect(0, 0, 16, 4), make([]uint32, 64)}
			for y := 0; y < 4; y++ {
				for x := 0; x < 16; x++ {
					f.pixels[y*16+x] = 0xff000000 | uint32(x/4)*0x123456
				}
			}
			v := new(glyphViewer)
			for range 3 {
				encodeGlyphTest(t, e, v, f)
				if e.bytes > e.maxBytes || len(e.entries) > e.maxEntries || len(e.seen) > 2*e.maxEntries {
					t.Fatal("cache exceeded budget")
				}
			}
			if v.resets == 0 {
				t.Fatal("cache did not exercise reset/ID reuse")
			}
		})
	}
}

func TestGlyphCacheNovelImageStaysOnePNGAndBoundsCandidates(t *testing.T) {
	e := newGlyphEncoder(2, 2, 0)
	e.maxEntries = 4
	f := frame{64, 32, image.Rect(0, 0, 64, 32), make([]uint32, 64*32)}
	for i := range f.pixels {
		f.pixels[i] = 0xff000000 | uint32(i)
	}
	v := new(glyphViewer)
	messages := encodeGlyphTest(t, e, v, f)
	if len(messages) != 1 || v.pngs != 1 || v.definitions != 0 || len(e.seen) > 8 {
		t.Fatal("one-off image tiles fragmented PNG damage or grew the cache")
	}
}

func TestGlyphCacheChecksPixelsOnHashCollision(t *testing.T) {
	e := newGlyphEncoder(2, 2, 0)
	f := frame{2, 2, image.Rect(0, 0, 2, 2), []uint32{0xff123456, 0xff234567, 0xff345678, 0xff456789}}
	e.entries[fingerprint(f, f.rect)] = cachedGlyph{1, make([]uint32, 4)}
	v := new(glyphViewer)
	encodeGlyphTest(t, e, v, f)
	if v.draws != 0 || v.pngs != 1 {
		t.Fatal("a hash collision substituted unrelated glyphs")
	}
}

func TestGlyphWarmupBatchesDefinitionsAndBoundsPNGDecodes(t *testing.T) {
	f := benchmarkGlyphFrame()
	e, v := newGlyphEncoder(16, 28, 0), new(glyphViewer)
	encodeGlyphTest(t, e, v, f)
	if v.definitions != 256 || v.atlases != 1 || v.pngs > maxFallbackPNGs {
		t.Fatalf("warm-up needs %d atlas and %d damage PNG decodes for %d glyphs", v.atlases, v.pngs, v.definitions)
	}
}

func TestGlyphAdmissionBudgetIsSharedAcrossDamageRectangles(t *testing.T) {
	f := benchmarkGlyphFrame()
	e, v := newGlyphEncoder(16, 28, 0), new(glyphViewer)
	budget := 17
	for range 2 {
		messages, err := e.encodeWithBudget(f, &budget)
		if err != nil {
			t.Fatal(err)
		}
		v.receive(t, messages)
		v.check(t, f)
	}
	if budget != 0 || v.definitions != 17 || v.atlases != 1 {
		t.Fatal("admission budget was lost between rectangles")
	}
}

func TestRepaintingUniqueImageTilesDoesNotWarmGlyphCache(t *testing.T) {
	e := newGlyphEncoder(2, 2, 0)
	f := frame{64, 32, image.Rect(0, 0, 64, 32), make([]uint32, 64*32)}
	for i := range f.pixels {
		f.pixels[i] = 0xff000000 | uint32(i)
	}
	v := new(glyphViewer)
	for range 3 {
		encodeGlyphTest(t, e, v, f)
	}
	if v.definitions != 0 || v.pngs != 3 {
		t.Fatal("static image repaints were converted into a glyph atlas")
	}
}
