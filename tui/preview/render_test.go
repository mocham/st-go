package preview

import (
	"bytes"
	"compress/zlib"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

func TestPreviewBodyFontIs80PercentOfPreviousGovim(t *testing.T) {
	snapshot, err := Load(filepath.Join(t.TempDir(), "font.tex"), fixtureReader(map[string]string{
		"font.tex": `\begin{document}Ordinary body text.\end{document}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	pdf, _, err := Render(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fontPattern := regexp.MustCompile(`/F\w+ ([0-9.]+) Tf`)
	for _, match := range regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`).FindAllSubmatch(pdf, -1) {
		reader, err := zlib.NewReader(bytes.NewReader(match[1]))
		if err != nil {
			continue
		}
		content, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, font := range fontPattern.FindAllSubmatch(content, -1) {
			size, _ := strconv.ParseFloat(string(font[1]), 64)
			if math.Abs(size-11.2*0.8*0.8) < 0.005 {
				return
			}
		}
	}
	t.Fatal("rendered PDF does not contain the body font at 80% of the previous govim size")
}

func TestPaneFontScaleMatchesGovimScreenSize(t *testing.T) {
	for _, layout := range []struct {
		widthMM        float64
		pane, terminal int
	}{
		{140, 60, 100},
		{169.333, 80, 80},
		{95.25, 45, 80},
		{1016, 240, 240},
		{60, 12, 80}, // minimum-sized page enlarged to leave room for margins.
	} {
		got := FontScaleForPane(layout.widthMM, layout.pane, layout.terminal) * float64(layout.pane) / layout.widthMM
		want := (0.8 * 0.8) * float64(layout.terminal*60/100) / 140
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("layout %+v: displayed font density = %g, want %g", layout, got, want)
		}
	}
	if FontScaleForPane(0, 80, 80) != FontScale || FontScaleForPane(140, 0, 0) != FontScale {
		t.Fatal("uninitialized pane did not use the standard preview font scale")
	}
}
