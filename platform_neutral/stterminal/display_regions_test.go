package stterminal

import (
	"bytes"
	"reflect"
	"testing"

	"st-go/config"
)

type regionTestHooks struct {
	mockHooks
	regions    []DisplayRegion
	images     []RegionImage
	targets    []RegionID
	cleared    RegionID
	metadata   []RegionDocumentQuery
	navigation []RegionNavigation
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
func (h *regionTestHooks) QueryRegionDocument(id RegionID, query RegionDocumentQuery) error {
	h.targets = append(h.targets, id)
	h.metadata = append(h.metadata, query)
	return nil
}
func (h *regionTestHooks) SetRegionScrollReporting(RegionID, bool) error { return nil }
func (h *regionTestHooks) NavigateRegion(id RegionID, event RegionNavigation) error {
	h.targets = append(h.targets, id)
	h.navigation = append(h.navigation, event)
	return nil
}

func TestRegionDocumentMetadataProtocol(t *testing.T) {
	h := &regionTestHooks{}
	core := NewTerm(config.Default(), h)
	core.Twrite([]byte("\x1bPregion secondary toc 17 0; region secondary text 18 3 3072\x1b\\"), false)
	want := []RegionDocumentQuery{{Kind: "toc", Token: 17}, {Kind: "text", Token: 18, Page: 3, Offset: 3072}}
	if !reflect.DeepEqual(h.metadata, want) {
		t.Fatalf("metadata queries = %#v", h.metadata)
	}
	for _, command := range []string{"secondary toc", "secondary toc 0 0", "secondary toc 1 -1", "secondary text 1 0 0", "secondary text 1 2", "secondary toc 1 999999999", "secondary text 999999999999999999999 1 0"} {
		if err := core.dslRegion(dslTokenize(command)); err == nil {
			t.Fatalf("accepted %q", command)
		}
	}
	if len(h.metadata) != len(want) {
		t.Fatal("malformed metadata command reached frontend")
	}
}

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
	core.Twrite([]byte("\x1bPregion secondary open '/tmp/flow.pdf' background '#f7f5ef' fit-width continuous\x1b\\"), false)
	if len(h.images) != 3 || !h.images[2].Continuous || !h.images[2].Options.FitWidth || h.images[2].Background != 0xfff7f5ef {
		t.Fatal("continuous fit-width view was not passed to the frontend")
	}
	core.Twrite([]byte("\x1bPregion secondary open '/tmp/image.png' viewer fit-width; region secondary fit height; region secondary fit original; region secondary zoomstep -1\x1b\\"), false)
	if len(h.images) != 4 || !h.images[3].Viewer || !h.images[3].Options.FitWidth {
		t.Fatal("raster viewer request was not passed to the frontend")
	}
	core.Twrite([]byte("\x1bPregion secondary open '/tmp/image.png' viewer zoom 175\x1b\\"), false)
	if len(h.images) != 5 || !h.images[4].Viewer || h.images[4].ViewerZoom != 175 || h.images[4].Options.FitContain || h.images[4].Options.FitWidth || h.images[4].Options.FitHeight {
		t.Fatal("direct raster viewer zoom was not passed to the frontend")
	}
	wantNavigation := []RegionNavigation{{Kind: "fit-height"}, {Kind: "fit-original"}, {Kind: "zoomstep", Value: -1}}
	if !reflect.DeepEqual(h.navigation, wantNavigation) {
		t.Fatalf("viewer navigation = %#v", h.navigation)
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
		"secondary open a.pdf continuous background", "secondary open a.pdf continuous background '#xyz123'",
		"secondary open a.pdf continuous background '#fff'", "secondary open a.pdf background '#ffffff'",
		"secondary open a.png viewer continuous", "secondary open a.png zoom", "secondary open a.png zoom 0", "secondary open a.png zoom 100", "secondary open a.png viewer zoom 6401",
		"secondary fit", "secondary fit contain", "secondary zoomstep nope",
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

type regionSearchCall struct {
	id        RegionID
	text      string
	direction int
	next      bool
}
type regionSearchTestHooks struct {
	regionTestHooks
	calls []regionSearchCall
}

func (h *regionSearchTestHooks) FindRegion(id RegionID, text string, direction int, next bool) error {
	h.calls = append(h.calls, regionSearchCall{id, text, direction, next})
	return nil
}

func TestRegionOccurrenceSearchProtocol(t *testing.T) {
	h := &regionSearchTestHooks{}
	core := NewTerm(config.Default(), h)
	core.Twrite([]byte("\x1bPregion secondary find forward 'café α'; region secondary find next 'café α'; region secondary find previous 'café α'; region secondary find clear\x1b\\"), false)
	want := []regionSearchCall{{SecondaryRegion, "café α", 1, false}, {SecondaryRegion, "café α", 1, true}, {SecondaryRegion, "café α", -1, true}, {SecondaryRegion, "", 1, false}}
	if !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("search calls = %+v", h.calls)
	}
	for _, command := range []string{"secondary find", "secondary find clear extra", "secondary find sideways text"} {
		if err := core.dslRegion(dslTokenize(command)); err == nil {
			t.Fatalf("accepted %q", command)
		}
	}
}
