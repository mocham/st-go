package main

import (
	_ "embed"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"st-go/config"
	"st-go/limits"
)

// This build-time copy is kept in sync with st's embedded font by make gxw.
//
//go:embed Monaco_Linux.ttf
var embeddedFont []byte

type fontSource struct{ fonts []*truetype.Font }

func configuredFonts(o options) (*fontSource, error) {
	cfg, err := config.LoadResolved(o.configPath)
	if err != nil {
		log.Printf("config: %v (using embedded)", err)
		cfg = config.Default()
	}
	primary := cfg.Font
	if o.fontPath != "" {
		primary = o.fontPath
	}
	return loadFontSource(primary)
}

// Match st's order: configured primary, bundled Monaco, then optional icon and
// CJK faces. Keep parsed fonts across resolution changes; only faces are resized.
func loadFontSource(primary string) (*fontSource, error) {
	s := &fontSource{}
	total := 0
	add := func(data []byte) error {
		if len(data) > limits.FileBytes-total {
			return fmt.Errorf("font inputs exceed %d bytes", limits.FileBytes)
		}
		parsed, err := parseFont(data)
		if err == nil {
			s.fonts = append(s.fonts, parsed)
			total += len(data)
		}
		return err
	}
	addFile := func(name string) error {
		var paths []string
		if !filepath.IsAbs(name) && !strings.ContainsRune(name, filepath.Separator) {
			if home, err := os.UserHomeDir(); err == nil {
				paths = append(paths, filepath.Join(home, ".local/share/fonts", name))
			}
		}
		paths = append(paths, name)
		var err error
		for _, path := range paths {
			var data []byte
			data, err = limits.ReadFile(path)
			if err == nil {
				return add(data)
			}
		}
		return err
	}
	if primary != "" && primary != "Monaco_Linux.ttf" {
		if err := addFile(primary); err != nil {
			return nil, fmt.Errorf("load font %q: %w", primary, err)
		}
	}
	if err := add(embeddedFont); err != nil {
		return nil, fmt.Errorf("load bundled Monaco: %w", err)
	}
	for _, name := range []string{"fontawesome-solid.ttf", "msyh.ttc"} {
		if err := addFile(name); err != nil && !os.IsNotExist(err) {
			log.Printf("optional font %s: %v", name, err)
		}
	}
	return s, nil
}

func parseFont(data []byte) (parsed *truetype.Font, err error) {
	defer func() {
		if problem := recover(); problem != nil {
			parsed, err = nil, fmt.Errorf("invalid TrueType font: %v", problem)
		}
	}()
	// Validate SFNT/TTC tables before entering the native Go FreeType parser.
	collection, err := sfnt.ParseCollection(data)
	if err != nil {
		return nil, err
	}
	if _, err := collection.Font(0); err != nil {
		return nil, err
	}
	parsed, err = truetype.Parse(data)
	if err == nil && parsed.FUnitsPerEm() <= 0 {
		return nil, fmt.Errorf("invalid font units per em")
	}
	return parsed, err
}

type fallbackFace struct {
	fonts   []*truetype.Font
	faces   []font.Face
	metrics font.Metrics
}

func (s *fontSource) face(pixels int) (*fallbackFace, error) {
	f := &fallbackFace{fonts: s.fonts}
	for _, parsed := range s.fonts {
		bounds := parsed.Bounds(fixed.I(pixels))
		if _, ok := limits.Product("font scratch mask", limits.GlyphCacheBytes/4, bounds.Max.X.Ceil()-bounds.Min.X.Floor(), bounds.Max.Y.Ceil()-bounds.Min.Y.Floor()); !ok {
			f.Close()
			return nil, fmt.Errorf("font bounds exceed bitmap limit")
		}
		face := truetype.NewFace(parsed, &truetype.Options{Size: float64(pixels), DPI: 72, Hinting: font.HintingFull, GlyphCacheEntries: 64})
		f.faces = append(f.faces, face)
		m := face.Metrics()
		if len(f.faces) == 1 {
			f.metrics = m
		} else {
			f.metrics.Ascent = max(f.metrics.Ascent, m.Ascent)
			f.metrics.Descent = max(f.metrics.Descent, m.Descent)
			f.metrics.Height = max(f.metrics.Height, m.Height)
		}
	}
	f.metrics.Height = max(f.metrics.Height, f.metrics.Ascent+f.metrics.Descent)
	return f, nil
}

// A font.Face may successfully draw .notdef, so test the cmap before choosing a
// face. Advances, bounds, kerning and drawing must use the same fallback face.
func (f *fallbackFace) index(r rune) int {
	for i, parsed := range f.fonts {
		if parsed.Index(r) != 0 {
			return i
		}
	}
	return 0
}

func (f *fallbackFace) Glyph(dot fixed.Point26_6, r rune) (image.Rectangle, image.Image, image.Point, fixed.Int26_6, bool) {
	return f.faces[f.index(r)].Glyph(dot, r)
}

func (f *fallbackFace) GlyphBounds(r rune) (fixed.Rectangle26_6, fixed.Int26_6, bool) {
	return f.faces[f.index(r)].GlyphBounds(r)
}

func (f *fallbackFace) GlyphAdvance(r rune) (fixed.Int26_6, bool) {
	return f.faces[f.index(r)].GlyphAdvance(r)
}

func (f *fallbackFace) Kern(a, b rune) fixed.Int26_6 {
	i := f.index(a)
	if i != f.index(b) {
		return 0
	}
	return f.faces[i].Kern(a, b)
}

func (f *fallbackFace) Metrics() font.Metrics { return f.metrics }

func (f *fallbackFace) Close() error {
	var first error
	for _, face := range f.faces {
		if err := face.Close(); first == nil {
			first = err
		}
	}
	f.faces = nil
	return first
}

func newFont(path string, pixels int) (font.Face, error) {
	source, err := loadFontSource(path)
	if err != nil {
		return nil, err
	}
	return source.face(pixels)
}
