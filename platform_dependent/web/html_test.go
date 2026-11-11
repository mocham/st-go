package web

import (
	"image"
	"testing"

	"st-go/platform_neutral/api"
)

func TestHTMLPaneUsesSurfaceGeometryVisibilityAndLifetime(t *testing.T) {
	d := testDriver(t)
	pane, err := d.NewSurface(image.Rect(4, 0, 16, 8), 0xff000000)
	if err != nil {
		t.Fatal(err)
	}
	s := pane.(*surface)
	d.frames()
	view := api.HTMLView{Document: &api.HTMLDocument{HTML: "<h1>preview</h1>", CSS: "h1{color:red}"}}
	if !s.PresentHTML(0, view) || len(d.frames()) != 0 {
		t.Fatal("HTML publication unexpectedly generated PNG damage")
	}
	version, m := d.htmlSnapshot(0)
	if m == nil || len(m.Overlays) != 1 || m.Overlays[0].X != 4 || m.Overlays[0].View.Document.HTML != view.Document.HTML {
		t.Fatal("HTML snapshot lost content or geometry", m)
	}
	s.Configure(image.Rect(6, 0, 16, 8), 0xff000000)
	version, m = d.htmlSnapshot(version)
	if m == nil || m.Overlays[0].X != 6 {
		t.Fatal("HTML pane did not follow resize")
	}
	s.SetVisible(false)
	version, m = d.htmlSnapshot(version)
	if m == nil || len(m.Overlays) != 0 {
		t.Fatal("hidden tab retained HTML overlay")
	}
	s.SetVisible(true)
	version, m = d.htmlSnapshot(version)
	if m == nil || len(m.Overlays) != 1 {
		t.Fatal("reactivation lost HTML document")
	}
	s.Close()
	_, m = d.htmlSnapshot(version)
	if m == nil || len(m.Overlays) != 0 {
		t.Fatal("closed region retained HTML overlay")
	}
}
