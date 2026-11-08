package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"st-go/config"
	"st-go/pdfmeta"
	"st-go/term"
)

func metadataFrames(t *testing.T, v *viewer, payload pdfmeta.Payload) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	r := v.pendingMetadata
	if r == nil {
		t.Fatal("no metadata request")
	}
	for offset := 0; offset < len(data); {
		end := min(len(data), offset+pdfmeta.ChunkBytes)
		next := end
		if end == len(data) {
			next = -1
		}
		v.reply(fmt.Sprintf("region secondary data %d %s %d %d %d %s", r.token, r.kind, r.page, offset, next, base64.StdEncoding.EncodeToString(data[offset:end])))
		offset = end
	}
}

func TestZatTOCNavigationAndChunkedMetadata(t *testing.T) {
	var output bytes.Buffer
	v := &viewer{out: &output, cols: 28, rows: 24, pages: 5, options: options{page: 1, zoom: 100}}
	v.key("\t")
	if v.panel != panelTOC || !strings.Contains(output.String(), "region split 65%") || !strings.Contains(output.String(), "toc 1 0") {
		t.Fatalf("TOC did not open: %q", output.String())
	}
	metadataFrames(t, v, pdfmeta.Payload{TOC: []pdfmeta.Entry{{Title: strings.Repeat("Heading ", 600), Page: 1}, {Title: "Second", Page: 3, Level: 1}, {Title: "External"}}})
	if !v.tocLoaded || v.pendingMetadata != nil || len(v.toc) != 3 || !strings.Contains(output.String(), "toc 1 3072") {
		t.Fatal("chunked TOC was not assembled")
	}
	output.Reset()
	v.key("down")
	v.key("\r")
	if !strings.Contains(output.String(), "region secondary page 3") {
		t.Fatal("TOC Enter did not navigate")
	}
	output.Reset()
	v.key("down")
	v.key("\r")
	if output.Len() != 0 || !strings.Contains(v.message, "no local") {
		t.Fatal("external bookmark was executed")
	}
	v.key("\t")
	if v.panel != panelNone || !strings.Contains(output.String(), "region stack 1") {
		t.Fatal("Tab did not close the TOC")
	}
}

type selectableTextHooks struct{ term.Hooks }

func (*selectableTextHooks) SetMode(bool, uint) {}
func (*selectableTextHooks) SetCursor(int) bool { return false }
func (*selectableTextHooks) StartDraw() bool    { return false }
func (*selectableTextHooks) ImageClearAll()     {}

func TestZatPageTextUsesSelectableTerminalCells(t *testing.T) {
	var output bytes.Buffer
	v := &viewer{out: &output, cols: 40, rows: 12, pages: 5, options: options{page: 2, zoom: 100}}
	v.key("backtab")
	if v.panel != panelText || !strings.Contains(output.String(), "\x1b[?1000l\x1b[?1006l") {
		t.Fatal("text mode did not disable application mouse reporting")
	}
	output.Reset()
	metadataFrames(t, v, pdfmeta.Payload{Text: "Selectable café 世界.\nSecond line.\x1b[31mnot a control sequence"})
	v.draw()
	if strings.Contains(output.String(), "\x1b[31m") {
		t.Fatal("PDF text injected terminal controls")
	}
	cfg := config.Default()
	cfg.Cols, cfg.Rows = 40, 12
	core := term.NewTerm(cfg, &selectableTextHooks{})
	core.Twrite(output.Bytes(), false)
	if !strings.Contains(core.LineText(2), "Selectable café 世界.") {
		t.Fatalf("extracted text was not drawn as real cells: %q", core.LineText(2))
	}
	core.SelStart(0, 2, 0)
	core.SelExtend(9, 2, term.SelRegular, 0)
	core.SelExtend(9, 2, term.SelRegular, 1)
	if core.GetSel() != "Selectable" {
		t.Fatalf("text selection = %q", core.GetSel())
	}
	// Frequent PDF status polls must not repaint the body and destroy an
	// active selection before the terminal's Ctrl+Shift+C action reads it.
	output.Reset()
	v.reply("region secondary status 2 5 0 100 0 0")
	v.message = "Status changed"
	v.draw()
	core.Twrite(output.Bytes(), false)
	if core.GetSel() != "Selectable" {
		t.Fatal("status polling destroyed page-text selection")
	}
	v.reply("region secondary status 3 5 100000 100 0 0")
	if v.pendingMetadata == nil || v.pendingMetadata.page != 3 {
		t.Fatal("text did not follow the current PDF page")
	}
}

func TestZatMetadataRejectsStaleAndMalformedReplies(t *testing.T) {
	v := &viewer{out: &bytes.Buffer{}, pages: 3, options: options{page: 1}}
	v.key("backtab")
	old := v.pendingMetadata.token
	v.reply("region secondary status 2 3 0 100 0 0")
	if v.pendingMetadata.token == old {
		t.Fatal("page change did not replace metadata token")
	}
	v.reply(fmt.Sprintf("region secondary data %d text 1 0 -1 e30=", old))
	if v.textPage != 0 {
		t.Fatal("stale page text was accepted")
	}
	metadataFrames(t, v, pdfmeta.Payload{Text: "Current page"})
	if v.textPage != 2 || v.pageText != "Current page" {
		t.Fatal("current page text was not accepted")
	}
	v.reply("region secondary status 3 3 0 100 0 0")
	stale := v.pendingMetadata.token
	v.reply("region secondary status 2 3 0 100 0 0")
	v.reply(fmt.Sprintf("region secondary data %d text 3 0 -1 e30=", stale))
	if v.pendingMetadata != nil || v.textPage != 2 || v.pageText != "Current page" {
		t.Fatal("a late reply replaced cached text after returning to its page")
	}
	v.key("backtab")
	v.key("backtab")
	v.textPage = 0
	v.syncPanel()
	v.pendingMetadata.started = time.Now().Add(-4 * time.Second)
	v.expireMetadata(time.Now())
	if v.pendingMetadata != nil || !strings.Contains(v.panelError, "unavailable") {
		t.Fatal("unsupported terminal metadata did not time out cleanly")
	}
}

func TestZatTOCMouseAndEmptyPanels(t *testing.T) {
	var output bytes.Buffer
	v := &viewer{out: &output, cols: 25, rows: 12, pages: 3, options: options{page: 1}}
	v.key("\t")
	metadataFrames(t, v, pdfmeta.Payload{TOC: []pdfmeta.Entry{{Title: "First", Page: 1}, {Title: "Second", Page: 2}}})
	output.Reset()
	v.key("mouse:<0;2;4M")
	if v.tocSelection.Index != 1 || !strings.Contains(output.String(), "region secondary page 2") {
		t.Fatal("TOC mouse click did not select and jump")
	}
	output.Reset()
	v.key("mouse:<0;2;8M")
	v.key("mouse:<32;2;3M")
	if output.Len() != 0 {
		t.Fatal("blank-row click or mouse motion navigated")
	}
	v.toc, v.panelDirty = nil, true
	v.draw()
	if !strings.Contains(output.String(), "No table of contents") {
		t.Fatal("missing empty-TOC message")
	}
	v.key("backtab")
	metadataFrames(t, v, pdfmeta.Payload{})
	output.Reset()
	v.draw()
	if !strings.Contains(output.String(), "No text returned") {
		t.Fatal("missing scanned-page message")
	}
}

func TestZatTextWrappingAndShiftTabParsing(t *testing.T) {
	if got := panelLines("ab世界cd", 4); !reflect.DeepEqual(got, []string{"ab世", "界cd"}) {
		t.Fatalf("wide text wrapping = %q", got)
	}
	if got := cleanPanelText("cafe\u0301\r\nA\tB\x1b\u202e"); got != "café\nA    B" {
		t.Fatalf("clean text = %q", got)
	}
	var parser inputParser
	var keys []string
	for _, b := range []byte("\t\x1b[Z\x1b[1;2Z") {
		parser.feed([]byte{b}, time.Now(), func(key string) { keys = append(keys, key) }, func(string) {})
	}
	if !reflect.DeepEqual(keys, []string{"\t", "backtab", "backtab"}) {
		t.Fatalf("Tab keys = %q", keys)
	}
}

func TestZatMalformedMetadataIsBounded(t *testing.T) {
	for name, payload := range map[string]string{
		"base64":       "region secondary data 1 text 1 0 -1 !bad!",
		"continuation": "region secondary data 1 text 1 0 10 e30=",
		"json":         "region secondary data 1 text 1 0 -1 bm90LWpzb24=",
		"chunk":        "region secondary data 1 text 1 0 -1 " + base64.StdEncoding.EncodeToString(make([]byte, pdfmeta.ChunkBytes+1)),
	} {
		t.Run(name, func(t *testing.T) {
			v := &viewer{out: &bytes.Buffer{}, pages: 1, options: options{page: 1}}
			v.key("backtab")
			v.reply(payload)
			if v.pendingMetadata != nil || v.panelError == "" || v.textPage != 0 {
				t.Fatal("malformed metadata was not rejected")
			}
		})
	}
}

func TestZatTextPaneDoesNotMistakeLoadingForMissingText(t *testing.T) {
	var output bytes.Buffer
	v := &viewer{out: &output, cols: 60, rows: 24, loading: true, options: options{page: 1, zoom: 100}}
	v.key("backtab")
	v.draw()
	if strings.Contains(output.String(), "No text returned") || !strings.Contains(output.String(), "Loading") {
		t.Fatalf("unqueried PDF was mislabeled as missing text: %q", output.String())
	}
	output.Reset()
	v.reply("region secondary status 1 10 0 100 0 0")
	v.draw()
	if v.pendingMetadata == nil || strings.Contains(output.String(), "No text returned") {
		t.Fatal("ready PDF was not queried before deciding whether it has text")
	}
	metadataFrames(t, v, pdfmeta.Payload{Text: "第一讲：自由模、核与余核"})
	output.Reset()
	v.draw()
	if !strings.Contains(output.String(), "自由模") || strings.Contains(output.String(), "No text returned") {
		t.Fatal("completed extraction did not replace the loading placeholder")
	}
	v.open("updated.pdf")
	output.Reset()
	v.draw()
	if strings.Contains(output.String(), "No text returned") || strings.Contains(output.String(), "自由模") {
		t.Fatal("reload displayed stale text or a premature missing-text message")
	}
	output.Reset()
	v.reply("region secondary status 1 10 0 100 0 1")
	v.draw()
	if !strings.Contains(output.String(), "PDF unavailable") || strings.Contains(output.String(), "No text returned") {
		t.Fatal("render failure was mislabeled as a page without text")
	}
}
