package frontend

import (
	"testing"

	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

type htmlTestHost struct {
	api.Host
	supported bool
}

func (h *htmlTestHost) SupportsHTML() bool { return h.supported }

func TestHTMLPreviewIsRetainedOnlyByCapableBackends(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	host := &htmlTestHost{}
	trm.host = host
	defer func() { trm.host = nil }()
	doc := api.HTMLDocument{HTML: "<p>native HTML</p>"}
	if err := trm.SetRegionHTML(term.SecondaryRegion, doc); err != nil || trm.displayRegions.secondary.html.Document != nil {
		t.Fatal("non-web backend retained HTML", err)
	}
	host.supported = true
	if err := trm.SetRegionHTML(term.SecondaryRegion, doc); err != nil {
		t.Fatal(err)
	}
	surface := trm.displayRegions.secondary
	if surface.html.Document == nil || surface.request != nil || len(surface.bitmap.pixels) != 0 {
		t.Fatal("HTML preview entered the pixel/PDF render path")
	}
	if err := trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "position", Value: 500000}); err != nil || surface.html.Position != 500000 {
		t.Fatal("HTML source-position navigation was lost", err)
	}
	for range 3 {
		trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "scroll", Value: 2})
	}
	if surface.html.Scroll != 6 {
		t.Fatal("coalescing lost relative HTML navigation")
	}
	before := surface.html.Scroll
	if err := trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "view", Value: 1000000}); err != nil || surface.html.Scroll-before != trm.displayRegions.viewport.Dy()/trm.ch {
		t.Fatal("HTML full-page forward navigation failed", err)
	}
	if err := trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "view", Value: -1000000}); err != nil || surface.html.Scroll != before {
		t.Fatal("HTML full-page backward navigation failed", err)
	}
	if err := trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "position", Value: 1000000}); err != nil || surface.html.Position != 1000000 || surface.html.Scroll != 0 {
		t.Fatal("HTML jump to end failed", err)
	}
	trm.ClearRegion(term.SecondaryRegion)
	if surface.html.Document != nil {
		t.Fatal("region clear retained HTML")
	}
}
