package stterminal

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"st-go/config"
	"st-go/platform_neutral/api"
)

type htmlRegionHooks struct {
	regionTestHooks
	document api.HTMLDocument
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
