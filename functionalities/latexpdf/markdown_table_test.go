package latexpdf

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestMarkdownTablesKeepStructureAlignmentAndInlineContent(t *testing.T) {
	source := "# Tools\n\n" + `\newcommand{\RR}{\mathbb{R}}` + "\n\n" +
		"| Tool | Command | Value |\n| :--- | :---: | ---: |\n" +
		"| **shell** | `a\\|b` | $f:\\RR\\to\\RR$ |\n" +
		"| [read](https://example.test) | first<br>second | 42 |\n" +
		"| escaped \\| pipe | ``x|y`` | $|x|$ |\n\nAfter table."
	result := NewRenderer().RenderHTML(MarkdownToLaTeX(source), "")
	body := string(result.Body)
	if result.MathErrors != 0 {
		t.Fatalf("table math errors: %d", result.MathErrors)
	}
	for _, want := range []string{"<table", "<thead>", "<tbody>", "<strong>shell</strong>", "<code>a|b</code>", "<code>x|y</code>", "first<br>second", "escaped | pipe", `style="text-align:right"`, `style="text-align:center"`, "After table."} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in table HTML: %s", want, body)
		}
	}
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			counts[node.Data]++
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if counts["table"] != 1 || counts["tr"] != 4 || counts["th"] != 3 || counts["td"] != 9 || counts["svg"] != 2 {
		t.Fatalf("table structure = %v: %s", counts, body)
	}
}

func TestMarkdownTablesBoundariesAndStreamingRows(t *testing.T) {
	for _, test := range []struct {
		name, source  string
		tables, cells int
	}{
		{"no outer pipes", "Name | Value\n--- | ---\nitem | 2", 1, 2},
		{"empty padded cells", "| A | B |\n| - | - |\n| one |\n| | |\n", 1, 4},
		{"excess cells", "| A | B |\n| --- | --- |\n| one | two | ignored |", 1, 2},
		{"partial streamed row", "| A | B |\n| --- | --- |\n| incoming", 1, 2},
		{"partial delimiter", "| A | B |\n| --- |", 0, 0},
		{"ordinary pipes", "foo | bar\nnot a delimiter", 0, 0},
		{"invalid alignment", "| A | B |\n| ::--- | --- |\n| one | two |", 0, 0},
		{"code fence", "```\n| A | B |\n| --- | --- |\n| one | two |\n```", 0, 0},
		{"long code fence", "````\n```\n| A | B |\n| --- | --- |\n| one | two |\n````", 0, 0},
		{"single column", "| Name |\n| --- |\n| value |", 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := string(NewRenderer().RenderHTML(MarkdownToLaTeX(test.source), "").Body)
			if strings.Count(body, "<table ") != test.tables || strings.Count(body, "<td ") != test.cells {
				t.Fatalf("table/cell count: %s", body)
			}
		})
	}
}
