package stterminal

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"st-go/config"
)

type statusTestHooks struct {
	regionTestHooks
	status RegionStatus
	calls  int
}

func (h *statusTestHooks) SetRegionStatus(id RegionID, status RegionStatus) error {
	h.status = status
	h.calls++
	return nil
}

func TestRegionStatusProtocol(t *testing.T) {
	h := &statusTestHooks{}
	core := NewTerm(config.Default(), h)
	status := RegionStatus{Items: []RegionStatusItem{{Text: "12345678 Ready", Color: 0xff5fd787}}}
	data, _ := json.Marshal(status)
	core.Twrite([]byte("hello\x1bPregion secondary status "+base64.StdEncoding.EncodeToString(data)+"\x1b\\"), false)
	if h.calls != 1 || h.status.Items[0] != status.Items[0] || core.CursorX() != 5 {
		t.Fatal("status/cursor mismatch")
	}
	for _, raw := range []string{`{"items":[{"text":"bad\u001b","color":4294967295}]}`, `{"items":[{"text":"bad","color":255}]}`} {
		if err := core.dslRegion([]string{"secondary", "status", base64.StdEncoding.EncodeToString([]byte(raw))}); err == nil {
			t.Fatal("invalid status accepted")
		}
	}
	core.Twrite([]byte("\x1bPregion secondary status clear\x1b\\"), false)
	if h.calls != 2 || len(h.status.Items) != 0 {
		t.Fatal("bar was not cleared")
	}
}
