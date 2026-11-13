package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/math/fixed"
	"st-go/widgets/systembar"
)

func TestBundledFontMatchesST(t *testing.T) {
	data, err := os.ReadFile("../../Monaco_Linux.ttf")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, embeddedFont) {
		t.Fatal("bundled Monaco is stale; run make gxw")
	}
}

func TestConfiguredFontAndCJKFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".local/share/fonts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../functionalities/latexpdf/pdf_assets/DroidSansFallbackFull.ttf")
	if err != nil {
		t.Fatal(err)
	}
	// Wrap the existing CJK fixture in a one-face TTC, exercising the same
	// collection format as st's optional msyh.ttc without a system font dependency.
	ttc := make([]byte, 16+len(data))
	copy(ttc, "ttcf")
	binary.BigEndian.PutUint32(ttc[4:], 0x10000)
	binary.BigEndian.PutUint32(ttc[8:], 1)
	binary.BigEndian.PutUint32(ttc[12:], 16)
	copy(ttc[16:], data)
	for i := 0; i < int(binary.BigEndian.Uint16(data[4:])); i++ {
		at := 16 + 12 + i*16 + 8
		binary.BigEndian.PutUint32(ttc[at:], binary.BigEndian.Uint32(ttc[at:])+16)
	}
	if err := os.WriteFile(filepath.Join(dir, "msyh.ttc"), ttc, 0600); err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "custom.ttf"), gomono.TTF, 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"font":"custom.ttf"}`), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := configuredFonts(options{configPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	face, err := source.face(32)
	if err != nil {
		t.Fatal(err)
	}
	defer face.Close()
	primary, _ := parseFont(gomono.TTF)
	if face.fonts[0].Name(1) != primary.Name(1) || face.index('A') != 0 {
		t.Fatal("relative configured font did not take precedence")
	}
	for _, r := range "世界日本語" {
		i := face.index(r)
		if i == 0 || face.fonts[i].Index(r) == 0 {
			t.Fatalf("%c did not use the CJK fallback", r)
		}
		bounds, advance, ok := face.GlyphBounds(r)
		got, advanceOK := face.GlyphAdvance(r)
		if !ok || !advanceOK || advance != got || bounds.Empty() || advance <= 0 {
			t.Fatalf("incorrect fallback metrics for %c", r)
		}
		rect, mask, origin, drawnAdvance, ok := face.Glyph(fixed.P(0, 40), r)
		if !ok || mask == nil || rect.Empty() || drawnAdvance != advance {
			t.Fatalf("missing CJK raster for %c", r)
		}
		ink := false
		for y := 0; y < rect.Dy(); y++ {
			for x := 0; x < rect.Dx(); x++ {
				_, _, _, a := mask.At(origin.X+x, origin.Y+y).RGBA()
				ink = ink || a != 0
			}
		}
		if !ink {
			t.Fatalf("blank CJK raster for %c", r)
		}
	}
	if face.Kern('A', '世') != 0 || font.MeasureString(face, "A世界").Ceil() <= font.MeasureString(face, "ABC").Ceil() {
		t.Fatal("mixed-language advances/kerning did not follow fallback selection")
	}
	// Explicit -font wins over the config, including a primary TTC face.
	override, err := configuredFonts(options{configPath: configPath, fontPath: filepath.Join(dir, "msyh.ttc")})
	if err != nil || override.fonts[0].Index('世') == 0 {
		t.Fatal("-font did not override the configuration", err)
	}
}

func TestResolutionChangesResizeFontsIconsAndWorkarea(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source, err := loadFontSource("")
	if err != nil {
		t.Fatal(err)
	}
	w := &wm{options: options{fontSize: 16}, fonts: source}
	defer func() {
		if w.face != nil {
			w.face.Close()
		}
	}()
	var smallHeight int
	for _, size := range []struct{ width, height, scale, systemHeight int }{
		{1200, 800, 1, 40}, {1201, 800, 2, 40}, {1280, 800, 2, 40}, {1281, 800, 2, 51}, {1200, 801, 2, 51}, {1920, 1080, 2, 51}, {1024, 768, 1, 40},
	} {
		old, oldScale := w.face, w.scale
		w.width, w.height = size.width, size.height
		if err := w.updateScale(); err != nil {
			t.Fatal(err)
		}
		if w.scale != size.scale {
			t.Fatalf("%dx%d scale=%d", size.width, size.height, w.scale)
		}
		if old != nil && oldScale != w.scale && len(old.(*fallbackFace).faces) != 0 {
			t.Fatal("resize retained the old rasterizer")
		}
		if size.scale == 1 {
			smallHeight = w.barHeight
		} else if d := w.barHeight - 2*smallHeight; d < -1 || d > 1 {
			t.Fatal("high-resolution tab font did not double", w.barHeight)
		}
		if w.barHeight != w.face.Metrics().Height.Ceil() {
			t.Fatal("top tabs retained extra vertical padding")
		}
		if w.systemStyle.Height() != size.systemHeight || len(w.systemStyle.Glyphs[systembar.NewTerminalIcon]) != w.systemStyle.CellWidth*w.systemStyle.CellHeight {
			t.Fatal("system bar does not use framebuffer sizing", w.systemStyle.Height())
		}
		area := w.workarea()
		if area != image.Rect(0, w.barHeight, w.width, w.height-size.systemHeight) {
			t.Fatal("workarea did not reserve the resized bars", area)
		}
	}
}
