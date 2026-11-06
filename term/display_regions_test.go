package term

import (
	"bytes"
	"reflect"
	"testing"

	"st-go/config"
)

type regionTestHooks struct {
	mockHooks
	regions []DisplayRegion
	images  []RegionImage
	targets []RegionID
	cleared RegionID
}

func (h *regionTestHooks) SetRegions(regions []DisplayRegion) error {
	h.regions = regions
	return nil
}
func (h *regionTestHooks) Regions() []DisplayRegion { return h.regions }
func (h *regionTestHooks) OpenRegion(id RegionID, req RegionImage) error {
	h.targets = append(h.targets, id)
	h.images = append(h.images, req)
	return nil
}
func (h *regionTestHooks) ClearRegion(id RegionID) error { h.cleared = id; return nil }

func TestDisplayRegionProtocol(t *testing.T) {
	h := &regionTestHooks{regions: []DisplayRegion{{MainRegion, CellRect{Width: 100, Height: 30}}}}
	core := NewTerm(config.Default(), h)
	var reply bytes.Buffer
	core.SetWriter(func(b []byte) { reply.Write(b) })
	core.Twrite([]byte("editor\x1bPregion split 40%; region secondary open '/tmp/a;b.pdf' page 3; region query\x1b\\"), false)
	want := []DisplayRegion{
		{MainRegion, CellRect{0, 0, 60, 30}},
		{SecondaryRegion, CellRect{60, 0, 40, 30}},
	}
	if !reflect.DeepEqual(h.regions, want) {
		t.Fatalf("layout = %#v", h.regions)
	}
	if len(h.images) != 1 || h.targets[0] != SecondaryRegion || h.images[0].Path != "/tmp/a;b.pdf" || h.images[0].Options.Page != 2 || !h.images[0].Options.FitContain {
		t.Fatalf("image requests = %#v", h.images)
	}
	if core.CursorX() != 6 || core.LineText(0)[:6] != "editor" {
		t.Fatal("preview protocol changed the editor cursor/text")
	}
	if got := reply.String(); got != "\x1bPregion layout main 1 1 60 30 secondary 61 1 40 30\x1b\\" {
		t.Fatalf("query reply = %q", got)
	}
	core.Twrite([]byte("\x1bPregion secondary open '/tmp/revised.pdf' page current\x1b\\"), false)
	if len(h.images) != 2 || !h.images[1].KeepPage {
		t.Fatal("page current was not passed to the frontend")
	}
	core.Twrite([]byte("\x1bPregion secondary clear; region reset\x1b\\"), false)
	if h.cleared != SecondaryRegion || h.regions != nil {
		t.Fatal("clear/reset was not dispatched")
	}
}

func TestDisplayRegionParserRejectsMalformedCommands(t *testing.T) {
	h := &regionTestHooks{regions: []DisplayRegion{{MainRegion, CellRect{Width: 80, Height: 24}}}}
	core := NewTerm(config.Default(), h)
	for _, command := range []string{
		"layout", "layout main 1 1 80", "layout main 0 1 80 24",
		"layout main 1 1 999999999999999999999999 24", "split 100%", "split 0", "split 80",
		"secondary open", "secondary open a.pdf page", "secondary open a.pdf page 0",
		"secondary open a.webp anim", "secondary open a.png unknown", "secondary clear extra", "reset extra",
	} {
		before := append([]DisplayRegion(nil), h.regions...)
		if err := core.dslRegion(dslTokenize(command)); err == nil {
			t.Errorf("accepted %q", command)
		}
		if !reflect.DeepEqual(before, h.regions) || len(h.images) != 0 {
			t.Fatalf("malformed command %q had side effects", command)
		}
	}
	if err := core.dslRegion(dslTokenize("layout main 1 1 50 24 secondary 51 1 30 24")); err != nil {
		t.Fatal(err)
	}
	if h.regions[1].Bounds.X != 50 {
		t.Fatal("explicit rectangles were not converted from one-based coordinates")
	}
}
