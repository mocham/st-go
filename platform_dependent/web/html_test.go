package web

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if m == nil || len(m.Overlays) != 1 || m.Overlays[0].X != 4 || m.Overlays[0].Document == "" || m.Overlays[0].View.Document != nil {
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

func TestHTMLResourceCachingAndNavigationBytes(t *testing.T) {
	d := testDriver(t)
	pane, err := d.NewSurface(image.Rect(0, 0, 16, 8), 0xff000000)
	if err != nil {
		t.Fatal(err)
	}
	s := pane.(*surface)
	doc := &api.HTMLDocument{HTML: strings.Repeat("<p>document body</p>", 65536), CSS: "p{color:red}"}
	view := api.HTMLView{Document: doc}
	s.PresentHTML(0, view)
	version, m := d.htmlSnapshot(0)
	id := m.Overlays[0].Document
	get := func(id, encoding, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/html/"+id, nil)
		r.Header.Set("Accept-Encoding", encoding)
		r.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()
		d.serveHTML(w, r, "html/"+id)
		return w
	}
	w := get(id, "gzip", "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(w.Header().Get("Cache-Control"), "private,") {
		t.Fatal(w.Code, w.Header())
	}
	z, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(z)
	z.Close()
	var got api.HTMLDocument
	if err != nil || json.Unmarshal(data, &got) != nil || got != *doc {
		t.Fatal("resource body changed")
	}
	if w.Body.Len() >= len(doc.HTML)/4 {
		t.Fatal("repetitive document did not compress")
	}
	cached := get(id, "gzip", w.Header().Get("ETag"))
	if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
		t.Fatal("conditional fetch retransmitted document")
	}
	var transferred int
	for i := 1; i <= 100; i++ {
		view.Scroll = i
		s.PresentHTML(0, view)
		version, m = d.htmlSnapshot(version)
		data, err := json.Marshal(m)
		if err != nil || m.Overlays[0].Document != id || bytes.Contains(data, []byte("document body")) {
			t.Fatal("navigation retransmitted content")
		}
		transferred += len(data)
	}
	if transferred > 65536 {
		t.Fatal("navigation payload grew with document size", transferred)
	}
	s.SetVisible(false)
	if get(id, "gzip;q=0", "").Header().Get("Content-Encoding") != "" {
		t.Fatal("ignored gzip opt-out")
	}
	if get(id, "", "").Code != http.StatusOK {
		t.Fatal("hidden tab lost resource")
	}
	s.SetVisible(true)
	view.Document = &api.HTMLDocument{HTML: doc.HTML, CSS: "p{color:blue}"}
	s.PresentHTML(0, view)
	_, m = d.htmlSnapshot(0)
	if m.Overlays[0].Document == id {
		t.Fatal("revision reused immutable URL")
	}
	if get(id, "", "").Code != http.StatusNotFound {
		t.Fatal("replaced resource retained by server")
	}
	s.Close()
	if get(m.Overlays[0].Document, "", "").Code != http.StatusNotFound {
		t.Fatal("closed surface retained resource")
	}
}
