package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"st-go/config"
	"st-go/pdfmeta"
	"st-go/term"
)

func TestSearchShorthand(t *testing.T) {
	for _, test := range [][2]string{
		{"{alpha} {Gamma} {omega} {varsigma}", "α Γ ω ς"},
		{`caf\'e \'{E} \` + "`a" + ` \"u \^o \~n`, "café É à ü ô ñ"},
		{`\v{c} \c{c} \H{o} \r{a}`, "č ç ő å"},
		{"cafe\u0301 α", "café α"},
		{`\{alpha\} \\'e {unknown} \unknown`, `{alpha} \'e {unknown} \unknown`},
		{`unfinished {alpha \'{`, `unfinished {alpha \'{`},
	} {
		if got := expandSearch(test[0]); got != test[1] {
			t.Errorf("expandSearch(%q)=%q, want %q", test[0], got, test[1])
		}
	}
}

func TestSearchPromptShortcutsAndOccurrenceReplies(t *testing.T) {
	var out bytes.Buffer
	v := &viewer{out: &out, pages: 3, options: options{page: 1, zoom: 100}}
	var parser inputParser
	for _, b := range []byte(`/caf\'e {alpha}` + "\r") {
		parser.feed([]byte{b}, time.Now(), v.key, v.reply)
	}
	v.key("n")
	v.key("N")
	for _, direction := range []string{"forward", "next", "previous"} {
		if !strings.Contains(out.String(), "find "+direction+" 'café α'") {
			t.Fatalf("missing normalized %s query: %q", direction, out.String())
		}
	}
	v.reply("region secondary matches 1 2 3 2 0")
	if v.searchPageMatch != 2 || !strings.Contains(v.message, "Match 2/3 on page 1") {
		t.Fatalf("occurrence reply = %q", v.message)
	}
	out.Reset()
	v.key("/")
	v.key("\r")
	if !strings.Contains(out.String(), "find forward 'café α'") {
		t.Fatal("empty / did not repeat the previous search")
	}
	v.execute("nohlsearch")
	if v.searchVisible || !strings.Contains(out.String(), "find clear") {
		t.Fatal("nohlsearch did not clear highlights")
	}
	out.Reset()
	v.search = strings.Repeat("{alpha}", 200)
	v.find(false, false)
	if !strings.Contains(out.String(), strings.Repeat("α", 200)) {
		t.Fatal("search budget was applied before shorthand expansion")
	}
	out.Reset()
	v.search = strings.Repeat("{alpha}", 600)
	v.find(false, false)
	if out.Len() != 0 || !strings.Contains(v.message, "limited") {
		t.Fatal("oversized expanded search was sent")
	}
}

func TestCaseFoldedTextHitsKeepOriginalOffsets(t *testing.T) {
	for _, test := range []struct {
		text, query string
		want        []textHit
	}{
		{"α Α α", "α", []textHit{{0, 2}, {3, 5}, {6, 8}}},
		{"Σσς", "σ", []textHit{{0, 2}, {2, 4}, {4, 6}}},
		{"CAFÉ café", "café", []textHit{{0, 5}, {6, 11}}},
		{"K k", "k", []textHit{{0, 3}, {4, 5}}},
		{"literal .+ .+", ".+", []textHit{{8, 10}, {11, 13}}},
	} {
		if got := textSearchHits(test.text, test.query); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("hits(%q,%q)=%v, want %v", test.text, test.query, got, test.want)
		}
	}
}

func TestTextSearchHighlightNavigationAndSelection(t *testing.T) {
	var out bytes.Buffer
	v := &viewer{out: &out, cols: 13, rows: 7, pages: 2, options: options{page: 1, zoom: 100}}
	v.key("backtab")
	metadataFrames(t, v, pdfmeta.Payload{Text: "café α\n" + strings.Repeat("filler\n", 12) + "CAFÉ α ending"})
	v.search = `caf\'e {alpha}`
	v.find(false, false)
	v.reply("region secondary matches 1 2 2 2 0")
	if len(v.textHits) != 2 || v.textTop == 0 {
		t.Fatalf("second occurrence was not revealed: hits=%v top=%d", v.textHits, v.textTop)
	}
	out.Reset()
	v.draw()
	if !strings.Contains(out.String(), "\x1b[30;48;5;214m") {
		t.Fatal("active occurrence was not highlighted")
	}
	cfg := config.Default()
	cfg.Cols, cfg.Rows = 13, 7
	core := term.NewTerm(cfg, &selectableTextHooks{})
	core.Twrite(out.Bytes(), false)
	row := -1
	for y := 2; y < core.Rows()-1; y++ {
		if strings.Contains(core.LineText(y), "CAFÉ α") {
			row = y
			break
		}
	}
	if row < 0 {
		t.Fatal("highlighted search result was not ordinary terminal text")
	}
	core.SelStart(0, row, 0)
	core.SelExtend(3, row, term.SelRegular, 0)
	core.SelExtend(3, row, term.SelRegular, 1)
	if core.GetSel() != "CAFÉ" {
		t.Fatalf("selected highlighted text = %q", core.GetSel())
	}
	out.Reset()
	v.reply("region secondary status 1 2 0 100 0 0")
	v.draw()
	core.Twrite(out.Bytes(), false)
	if core.GetSel() != "CAFÉ" {
		t.Fatal("status polling repainted the highlighted selection")
	}
	// Reflow can split an occurrence, but both halves must remain highlighted.
	v.cols = 6
	v.reflowText()
	v.panelDirty = true
	out.Reset()
	v.draw()
	if strings.Count(out.String(), "\x1b[30;48;5;214m") < 2 {
		t.Fatal("soft-wrapped occurrence lost a highlight segment")
	}
}

func TestTextMetadataWaitsForNativeSearch(t *testing.T) {
	v := &viewer{out: &bytes.Buffer{}, pages: 2, options: options{page: 1, zoom: 100}}
	v.key("backtab")
	v.search = "needle"
	v.find(false, false)
	v.pendingMetadata.started = time.Now().Add(-4 * time.Second)
	v.expireMetadata(time.Now())
	if v.pendingMetadata == nil || v.panelError != "" {
		t.Fatal("metadata queued behind search was incorrectly timed out")
	}
	v.reply("region secondary matches 1 1 1 1 0")
	v.expireMetadata(time.Now())
	if v.searchPending || v.pendingMetadata == nil || v.panelError != "" {
		t.Fatal("search completion did not restart the metadata response deadline")
	}
	metadataFrames(t, v, pdfmeta.Payload{Text: "needle"})
	if len(v.textHits) != 1 {
		t.Fatal("delayed page text did not acquire search highlights")
	}
}
