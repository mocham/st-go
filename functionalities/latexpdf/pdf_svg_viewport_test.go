package latexpdf

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestPDFNestedSVGViewportCoordinatesAndClips(t *testing.T) {
	// This is the same nested-viewport technique MathJax uses for stretch
	// extenders, with small coordinates so the expected geometry is explicit.
	source := `<svg viewBox="0 0 200 200"><g transform="translate(10,20) scale(2)">
<svg x="30" y="40" width="20" height="10" viewBox="5 7 10 5">
<path d="M 5 7 L 15 12"/>
<svg x="6" y="8" width="2" height="1" viewBox="0 0 2 1"><path d="M 0 0 L 2 1"/></svg>
<path d="M 5 7 L 15 12"/>
</svg><path d="M 0 0 L 1 1"/></g></svg>`
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	formula, err := newPDFRenderer().formulaFromWrapper(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(formula.paths) != 4 {
		t.Fatalf("got %d paths, want 4", len(formula.paths))
	}
	for i, want := range [][5]float64{{70, 100, 110, 120, 1}, {74, 104, 82, 108, 2}, {70, 100, 110, 120, 1}, {10, 20, 12, 22, 0}} {
		path := formula.paths[i]
		if len(path.clips) != int(want[4]) || path.Segments[0].Arg[0] != want[0] || path.Segments[0].Arg[1] != want[1] || path.Segments[1].Arg[0] != want[2] || path.Segments[1].Arg[1] != want[3] {
			t.Errorf("path %d lost its viewport transform or clip stack: %+v", i, path)
		}
		if len(path.clips) > 0 {
			clip := path.clips[0]
			if clip[0].X != 70 || clip[0].Y != 100 || clip[2].X != 110 || clip[2].Y != 120 {
				t.Errorf("outer viewport clip changed in a descendant: %+v", clip)
			}
		}
	}
}

func TestPDFStretchyBraceExtenders(t *testing.T) {
	renderer := NewRenderer()
	for _, source := range []string{
		`\[\underbrace{a+b+c+d+e+f+g+h}_{n\text{ 次}}\]`,
		`\[\overbrace{a+b+c+d+e+f+g+h}^{n}\]`,
		`\[\underbrace{\underbrace{a+b+c}_{m}+d+e}_{n}\]`,
	} {
		result := renderer.RenderHTML(source, "")
		if result.MathErrors != 0 {
			t.Fatal(result.MathErrorExpressions)
		}
		root, err := html.Parse(strings.NewReader(string(result.Body)))
		if err != nil {
			t.Fatal(err)
		}
		formula, err := newPDFRenderer().formulaFromWrapper(findMathWrapper(root, "math-display"))
		if err != nil {
			t.Fatal(err)
		}
		clipped := 0
		for _, path := range formula.paths {
			if len(path.clips) == 0 {
				continue
			}
			clipped++
			clip := path.clips[0]
			left, right := math.Min(clip[0].X, clip[2].X), math.Max(clip[0].X, clip[2].X)
			if left < formula.viewX || right > formula.viewX+formula.viewWidth || right <= left {
				t.Errorf("brace extender clip is outside the formula: %+v", clip)
			}
			// The stretched glyph must cover its entire viewport. A missing
			// viewBox translation leaves the right-hand joining segment blank.
			minX, maxX := math.Inf(1), math.Inf(-1)
			for _, segment := range path.Segments {
				if segment.Cmd == 'M' || segment.Cmd == 'L' {
					minX, maxX = math.Min(minX, segment.Arg[0]), math.Max(maxX, segment.Arg[0])
				}
			}
			if minX > left || maxX < right {
				t.Errorf("extender leaves a gap: glyph %g..%g, viewport %g..%g", minX, maxX, left, right)
			}
		}
		if clipped < 2 {
			t.Fatalf("long brace has only %d clipped extenders", clipped)
		}
		pdf, err := renderer.RenderPDF(Document{Source: source})
		if err != nil {
			t.Fatal(err)
		}
		content := inflatedPDFStreams(pdf)
		if bytes.Count(content, []byte("h W n")) != clipped {
			t.Fatal("PDF did not apply the brace extender clipping regions")
		}
		if bytes.Contains(pdf, []byte("/Subtype /Image")) {
			t.Fatal("brace rendering fell back to a bitmap")
		}
	}
}
