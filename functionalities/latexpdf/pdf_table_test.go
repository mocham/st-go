package latexpdf

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func tablePDFText(text string) []byte {
	var encoded []byte
	for _, r := range text {
		encoded = append(encoded, byte(r>>8), byte(r))
	}
	return encoded
}

func TestPDFMarkdownTableKeepsColumnsAlignedAndTextSelectable(t *testing.T) {
	source := "| Name | Command | Result |\n| :--- | :---: | ---: |\n| FIRST | CENTER | 12345 |\n| SECOND | centered | 6 |\n"
	result, err := NewRenderer().RenderPDFResultWithOptions(Document{Source: MarkdownToLaTeX(source)}, PDFOptions{PageWidth: 140, Compact: true, FontScale: 0.6848})
	if err != nil || result.MathErrors != 0 {
		t.Fatalf("render: %v, math errors %d", err, result.MathErrors)
	}
	content := inflatedPDFStreams(result.PDF)
	position := func(text string) (float64, float64) {
		t.Helper()
		end := bytes.Index(content, tablePDFText(text))
		if end < 0 {
			t.Fatalf("missing selectable text %q", text)
		}
		start := bytes.LastIndex(content[:end], []byte("BT "))
		match := regexp.MustCompile(`BT ([0-9.]+) ([0-9.]+) Td`).FindSubmatch(content[start:end])
		if len(match) != 3 {
			t.Fatalf("missing text position for %s", text)
		}
		x, _ := strconv.ParseFloat(string(match[1]), 64)
		y, _ := strconv.ParseFloat(string(match[2]), 64)
		return x, y
	}
	x1, y1 := position("FIRST")
	x2, y2 := position("CENTER")
	x3, y3 := position("12345")
	if !(x1 < x2 && x2 < x3) || y1 != y2 || y2 != y3 {
		t.Fatalf("table row flattened/misaligned: (%g,%g) (%g,%g) (%g,%g)", x1, y1, x2, y2, x3, y3)
	}
	x4, y4 := position("6")
	if x4 <= x3 || y4 >= y3 {
		t.Fatal("right alignment or row spacing was lost")
	}
	// Each of the nine cells has an actual vector border.
	if bytes.Count(content, []byte(" re ")) < 9 {
		t.Fatal("table grid was not drawn")
	}
}

func TestPDFMarkdownTableWrapsAndPaginatesTallCellsWithoutLosingText(t *testing.T) {
	var source strings.Builder
	source.WriteString("| HeaderLeft | HeaderRight |\n| --- | --- |\n")
	for row := 0; row < 24; row++ {
		fmt.Fprintf(&source, "| ROW%03d | %s END%03d |\n", row, strings.Repeat("wrapping words ", 7), row)
	}
	source.WriteString("| TALLROW | ")
	for token := 0; token < 200; token++ {
		fmt.Fprintf(&source, "token%03d ", token)
	}
	source.WriteString("|\n\nAftertable.")
	options := PDFOptions{PageWidth: 90, PageHeight: 65, Compact: true, FontScale: 0.6848}
	result, err := NewRenderer().RenderPDFResultWithOptions(Document{Source: MarkdownToLaTeX(source.String())}, options)
	if err != nil || result.MathErrors != 0 {
		t.Fatalf("render: %v", err)
	}
	content := inflatedPDFStreams(result.PDF)
	pages := len(regexp.MustCompile(`/Type\s*/Page\b`).FindAll(result.PDF, -1))
	if pages < 3 || pages > 40 {
		t.Fatalf("unexpected table pagination: %d pages", pages)
	}
	if count := bytes.Count(content, tablePDFText("HeaderLeft")); count < pages-1 {
		t.Fatalf("only %d repeated headers for %d pages", count, pages)
	}
	for row := 0; row < 24; row++ {
		for _, prefix := range []string{"ROW", "END"} {
			text := fmt.Sprintf("%s%03d", prefix, row)
			if bytes.Count(content, tablePDFText(text)) != 1 {
				t.Fatalf("lost/duplicated cell %q", text)
			}
		}
	}
	for token := 0; token < 200; token++ {
		text := fmt.Sprintf("token%03d", token)
		if bytes.Count(content, tablePDFText(text)) != 1 {
			t.Fatalf("lost/duplicated tall cell text %q", text)
		}
	}
	if bytes.Count(content, tablePDFText("Aftertable.")) != 1 {
		t.Fatal("following paragraph lost")
	}
	for _, match := range regexp.MustCompile(`BT ([0-9.]+) ([0-9.]+) Td`).FindAllSubmatch(content, -1) {
		x, _ := strconv.ParseFloat(string(match[1]), 64)
		y, _ := strconv.ParseFloat(string(match[2]), 64)
		if x < 3*72/25.4-0.1 || x > 87*72/25.4+0.1 || y < 2*72/25.4 || y > 63*72/25.4 {
			t.Fatalf("text outside preview page: x=%g y=%g", x, y)
		}
	}
}
