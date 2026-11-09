package term

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"st-go/config"
)

type listTestHooks struct {
	regionTestHooks
	list  *RegionList
	calls int
}

func (h *listTestHooks) ShowRegionList(id RegionID, list *RegionList) error {
	h.list = list
	h.calls++
	return nil
}

func TestRetainedListProtocol(t *testing.T) {
	h := &listTestHooks{}
	core := NewTerm(config.Default(), h)
	list := RegionList{Token: 17, Title: "Table of contents", Items: []RegionListItem{{ID: 1, Level: 4, Text: "label: sec:x"}}}
	data, _ := json.Marshal(list)
	core.Twrite([]byte("hello\x1bPregion secondary list "+base64.StdEncoding.EncodeToString(data)+"\x1b\\"), false)
	if h.calls != 1 || h.list == nil || h.list.Items[0] != list.Items[0] || core.CursorX() != 5 || core.LineText(0)[:5] != "hello" {
		t.Fatalf("list/cursor = %+v, %d", h.list, core.CursorX())
	}
	core.Twrite([]byte("\x1bPregion secondary list restore\x1b\\"), false)
	if h.calls != 2 || h.list != nil {
		t.Fatal("restore did not dispatch")
	}
	for _, raw := range []string{`{}`, `{"token":1,"title":"bad\u001b","items":[]}`, `{"token":1,"items":[{"id":1,"text":"a"},{"id":1,"text":"b"}]}`, `{"token":1,"items":[{"id":1,"level":99}]}`} {
		if err := core.dslRegion([]string{"secondary", "list", base64.StdEncoding.EncodeToString([]byte(raw))}); err == nil {
			t.Fatalf("accepted invalid list: %s", raw)
		}
	}
	for _, args := range [][]string{{"secondary", "list"}, {"secondary", "list", "%%%"}, {"secondary", "list", "restore", "extra"}} {
		if core.dslRegion(args) == nil {
			t.Fatalf("accepted invalid command: %+v", args)
		}
	}
	if h.calls != 2 {
		t.Fatal("invalid list reached frontend")
	}
}
