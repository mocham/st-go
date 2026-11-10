package imap

import (
	"encoding/json"
	"strings"
	"testing"
)

func testSet(t *testing.T) *Set {
	t.Helper()
	set, err := Parse([]byte(`{
 "filetypes":[".tex"],"placeholder":"<++>","defaults":{"not_after":["\\"]},
 "imap":[{"lhs":"oo","rhs":"short"},{"lhs":"foo","rhs":"long"},
 {"lhs":"αβ","rhs":"Unicode"},{"lhs":"aa","rhs":"first<++>second<++>"},
 {"lhs":"XX","rhs":"unguarded","not_after":[]}],
 "templates":{"environment":"\\begin{${name}}\n<++>\n\\end{${name}}<++>"}
}`))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestLongestSuffixGuardsAndUnicode(t *testing.T) {
	set := testSet(t)
	for _, test := range []struct {
		before, key, want string
		remove            int
		ok                bool
	}{
		{"世界fo", "o", "long", 2, true}, {"o", "o", "short", 1, true}, {"α", "β", "Unicode", 1, true},
		{"\\fo", "o", "short", 1, true}, // longer rule is guarded; shorter still applies
		{"\\o", "o", "", 0, false}, {"\\X", "X", "unguarded", 1, true}, {"f", "oo", "", 0, false},
	} {
		match, ok := set.Match([]rune(test.before), test.key)
		if ok != test.ok || ok && (match.Text != test.want || match.Remove != test.remove) {
			t.Errorf("%q + %q = %+v,%v", test.before, test.key, match, ok)
		}
	}
	if !set.Supports("/synthetic/FILE.TEX") || set.Supports("notes.md") {
		t.Fatal("filetype matching is incorrect")
	}
}

func TestTemplateCursorStopsAndLiteralVariables(t *testing.T) {
	set := testSet(t)
	match, ok := set.Match([]rune("a"), "a")
	if !ok || match.Text != "firstsecond<++>" || match.Cursor != 5 {
		t.Fatalf("mapping stop: %+v %v", match, ok)
	}
	expansion, err := set.Template("environment", map[string]string{"name": "cases"}, "  ")
	want := "  \\begin{cases}\n  \n  \\end{cases}<++>"
	if err != nil || expansion.Text != want || expansion.Cursor != len("  \\begin{cases}\n  ") {
		t.Fatalf("template %+v err=%v", expansion, err)
	}
	if _, err := set.Template("environment", nil, ""); err == nil {
		t.Fatal("missing variable accepted")
	}
	if _, err := set.Template("unknown", nil, ""); err == nil {
		t.Fatal("missing template accepted")
	}
	expansion, err = set.Template("environment", map[string]string{"name": "${not_an_expression}"}, "")
	if err != nil || !strings.Contains(expansion.Text, "${not_an_expression}") {
		t.Fatal("variable contents were evaluated")
	}
	if _, err := set.Template("environment", map[string]string{"name": strings.Repeat("x", maxText)}, strings.Repeat(" ", maxText)); err == nil {
		t.Fatal("unbounded expansion accepted")
	}
}

func TestNextPlaceholderUnderCursorForwardAndWrap(t *testing.T) {
	set := testSet(t)
	text := []rune("世界<++>mid<++>")
	for _, test := range []struct{ pos, want int }{{0, 2}, {2, 2}, {5, 2}, {6, 9}, {13, 2}, {-1, 2}, {100, 2}} {
		first, last, ok := set.NextPlaceholder(text, test.pos)
		if !ok || first != test.want || last-first != 4 {
			t.Errorf("pos %d -> %d,%d,%v", test.pos, first, last, ok)
		}
	}
	if _, _, ok := set.NextPlaceholder([]rune("none"), 0); ok {
		t.Fatal("invented a placeholder")
	}
	if _, _, ok := new(Set).NextPlaceholder(text, 0); ok {
		t.Fatal("zero-value set invented a placeholder")
	}
}

func TestInvalidConfigurations(t *testing.T) {
	for _, data := range []string{
		`{}`, `{"placeholder":"<++>","unknown":true}`, `{"placeholder":"<++>"} {}`,
		`{"placeholder":"<++>","imap":[{"lhs":"a","rhs":"one"},{"lhs":"a","rhs":"two"}]}`,
		`{"placeholder":"<++>","imap":[{"lhs":"","rhs":"empty"}]}`,
		`{"placeholder":"<++>","imap":[{"lhs":"a","rhs":"x","not_after":[""]}]}`,
		`{"placeholder":"<++>","templates":{"x":"${oops"}}`,
		`{"placeholder":"<++>","filetypes":["/tex"]}`,
	} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Errorf("accepted %s", data)
		}
	}
	data, _ := json.Marshal(config{Placeholder: "<++>", IMAP: []rule{{LHS: strings.Repeat("x", 257), RHS: "too long"}}})
	if _, err := Parse(data); err == nil {
		t.Fatal("unbounded trigger accepted")
	}
	if _, err := Parse([]byte{0xff}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}
