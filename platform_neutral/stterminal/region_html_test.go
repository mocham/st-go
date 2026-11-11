package stterminal

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"st-go/config"
	"st-go/limits"
	"st-go/platform_neutral/api"
)

type htmlRegionHooks struct {
	regionTestHooks
	document api.HTMLDocument
}

func TestLargeRegionHTMLCommitsAtomicallyAcrossPTYReads(t *testing.T) {
	h := &htmlRegionHooks{document: api.HTMLDocument{HTML: "previous preview"}}
	core := NewTerm(config.Default(), h)
	document := api.HTMLDocument{HTML: strings.Repeat("<p>世界;\x1b\\</p>\n", limits.ControlBytes/10), CSS: "p{color:red}"}
	data, _ := json.Marshal(document)
	if len(data) <= limits.ControlBytes {
		t.Fatal("fixture does not exceed the control limit")
	}
	core.Twrite([]byte("before"), false)
	feed := func(command string) {
		t.Helper()
		frame := []byte("\x1bPregion secondary html " + command + "\x1b\\")
		for len(frame) > 0 {
			n := min(7919, len(frame)) // split base64 and DCS terminators across reads
			core.Twrite(frame[:n], false)
			frame = frame[n:]
		}
	}
	feed(fmt.Sprintf("begin %d", len(data)))
	for offset := 0; offset < len(data); offset += 48000 {
		feed(fmt.Sprintf("chunk %d %s", offset, base64.StdEncoding.EncodeToString(data[offset:min(offset+48000, len(data))])))
		if len(h.targets) != 0 || h.document.HTML != "previous preview" {
			t.Fatal("partial transfer replaced the visible document")
		}
	}
	feed("commit")
	core.Twrite([]byte("after"), false)
	if h.document != document || len(h.targets) != 1 || core.htmlTransfer != nil || core.CursorX() != 11 || core.LineText(0)[:11] != "beforeafter" {
		t.Fatal("large HTML transfer changed pixels/text, lost content, or retained staging memory")
	}
}

func TestRegionHTMLRejectsBrokenTransfers(t *testing.T) {
	for _, args := range [][]string{
		{"begin", "-1"}, {"begin", strconv.Itoa(limits.FileBytes + 1)},
		{"chunk", "1", "e30="}, {"chunk", "-1", "e30="},
		{"chunk", "0", "?"}, {"chunk", "0", "e30g"}, {"commit"},
	} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			h := &htmlRegionHooks{}
			core := NewTerm(config.Default(), h)
			if err := core.dslRegionHTML(SecondaryRegion, []string{"begin", "2"}); err != nil {
				t.Fatal(err)
			}
			if err := core.dslRegionHTML(SecondaryRegion, args); err == nil || core.htmlTransfer != nil || len(h.targets) != 0 {
				t.Fatal("invalid transfer was retained or published", err)
			}
		})
	}
}

func TestRegionHTMLTransferResetAndReplacement(t *testing.T) {
	for _, reset := range []string{"\x1bPregion reset\x1b\\", "\x1bPregion secondary clear\x1b\\", "\x1bc", "\x1b[?1049h"} {
		core := NewTerm(config.Default(), &htmlRegionHooks{})
		core.dslRegionHTML(SecondaryRegion, []string{"begin", "2"})
		core.dslRegionHTML(SecondaryRegion, []string{"chunk", "0", "e30="})
		core.Twrite([]byte(reset), false)
		if core.htmlTransfer != nil {
			t.Fatalf("reset %q retained pending HTML", reset)
		}
	}
	h := &htmlRegionHooks{}
	core := NewTerm(config.Default(), h)
	core.dslRegionHTML(SecondaryRegion, []string{"begin", "123"})
	core.dslRegionHTML(SecondaryRegion, []string{"begin", "2"})
	core.dslRegionHTML(SecondaryRegion, []string{"chunk", "0", "e30="})
	if err := core.dslRegionHTML(SecondaryRegion, []string{"commit"}); err != nil || len(h.targets) != 1 || core.htmlTransfer != nil {
		t.Fatal("new transfer did not replace an interrupted transfer", err)
	}
}

type nativeHTMLHooks struct{ htmlRegionHooks }

func (*nativeHTMLHooks) SupportsHTML() bool { return false }

func TestNativeBackendDiscardsHTMLChunksWithoutRetainingData(t *testing.T) {
	h := &nativeHTMLHooks{}
	core := NewTerm(config.Default(), h)
	for _, args := range [][]string{{"begin", strconv.Itoa(limits.FileBytes)}, {"chunk", "0", "e30="}, {"commit"}} {
		if err := core.dslRegionHTML(SecondaryRegion, args); err != nil || core.htmlTransfer != nil || len(h.targets) != 0 {
			t.Fatal("native backend retained browser-only content", err)
		}
	}
}

func (h *htmlRegionHooks) SetRegionHTML(id RegionID, document api.HTMLDocument) error {
	h.targets = append(h.targets, id)
	h.document = document
	return nil
}

func TestRegionHTMLFramingPreservesTerminalState(t *testing.T) {
	h := &htmlRegionHooks{}
	core := NewTerm(config.Default(), h)
	document := api.HTMLDocument{HTML: "<h1 title='a;b'>世界</h1>\x1b[2J", CSS: "h1{color:red;background:white}"}
	data, _ := json.Marshal(document)
	core.Twrite([]byte("before\x1bPregion secondary html "+base64.StdEncoding.EncodeToString(data)+"\x1b\\after"), false)
	if h.document != document || len(h.targets) != 1 || h.targets[0] != SecondaryRegion || core.CursorX() != 11 || core.LineText(0)[:11] != "beforeafter" {
		t.Fatal("HTML payload changed the terminal or lost framing")
	}
	for _, payload := range []string{"?", base64.StdEncoding.EncodeToString([]byte(`{"html":1}`))} {
		if core.dslRegionHTML(SecondaryRegion, []string{payload}) == nil {
			t.Fatal("invalid payload reached HTML hook")
		}
	}
	unsupported := NewTerm(config.Default(), &regionTestHooks{})
	if err := unsupported.dslRegionHTML(SecondaryRegion, []string{"ignored"}); err != nil {
		t.Fatal("unsupported frontend did not discard HTML extension")
	}
}
