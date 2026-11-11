package web

import (
	"bytes"
	"image"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"st-go/platform_neutral/api"
)

func TestSceneRetainsEveryMutationAcrossCoalescingAndVisibility(t *testing.T) {
	d := testDriver(t)
	s, err := d.NewSceneSurface(api.TerminalWidget, image.Rect(0, 0, 16, 8), 0xff000000, true)
	if err != nil {
		t.Fatal(err)
	}
	row := api.SceneRow{Y: 0, Height: 2, Text: []api.TextRun{{Text: "old", Advance: 2}}}
	s.Apply(api.SceneUpdate{Rows: []api.SceneRow{row}})
	row.Text[0].Text = "caller mutated"
	s.Apply(api.SceneUpdate{Rows: []api.SceneRow{{Y: 2, Height: 2, Text: []api.TextRun{{Text: "second"}}}}})
	first := d.sceneSnapshot(false)
	if first == nil || len(first.Scenes) != 1 || len(first.Scenes[0].Update.Rows) != 2 || first.Scenes[0].Update.Rows[0].Text[0].Text != "old" {
		t.Fatalf("lost or aliased semantic state: %+v", first)
	}
	s.Apply(api.SceneUpdate{Rows: []api.SceneRow{{Y: 0, Height: 2, Text: []api.TextRun{{Text: "new"}}}}})
	s.SetVisible(false)
	d.sceneSnapshot(false)
	s.SetVisible(true)
	restored := d.sceneSnapshot(false)
	if !restored.Scenes[0].Update.Reset || len(restored.Scenes[0].Update.Rows) != 2 || restored.Scenes[0].Update.Rows[0].Text[0].Text != "new" {
		t.Fatal("visibility discarded retained rows")
	}
	if len(d.pixels) != 0 {
		t.Fatal("semantic terminal retained a server framebuffer")
	}
	s.Close()
	closed := d.sceneSnapshot(false)
	if !closed.Scenes[0].Hidden || len(d.scenes) != 0 {
		t.Fatal("closed scene survived")
	}
}

func TestEncodedMediaRangeLifetimeAndCancelledQuery(t *testing.T) {
	d := testDriver(t)
	data := []byte("%PDF-1.7\nimmutable original bytes\n%%EOF\n")
	id := d.RegisterMedia(api.MediaSource{Data: data})
	d.mu.Lock()
	resource := d.media[id]
	d.mu.Unlock()
	select {
	case <-resource.ready:
	case <-time.After(time.Second):
		t.Fatal("loader blocked")
	}
	path := "media/" + strconv.FormatUint(id, 10)
	r := httptest.NewRequest("GET", "http://localhost/"+path, nil)
	r.Header.Set("Range", "bytes=2-8")
	w := httptest.NewRecorder()
	d.serveMedia(w, r, path)
	if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), data[2:9]) {
		t.Fatalf("range=%d %q", w.Code, w.Body.Bytes())
	}
	called := false
	cancel := d.QueryMedia(id, api.DocumentQuery{Kind: "text", Page: 1}, func([]byte, error) { called = true })
	c := &client{}
	query := d.documentSnapshot(c).Documents[0]
	cancel()
	if err := d.documentReply(c, query.ID, []byte(`{"text":"stale"}`)); err != nil || called {
		t.Fatal("cancelled document completion was delivered", err)
	}
	d.ReleaseMedia(id)
	w = httptest.NewRecorder()
	d.serveMedia(w, r, path)
	if w.Code != 404 || d.mediaBytes != 0 {
		t.Fatal("released resource remained reachable")
	}
}
