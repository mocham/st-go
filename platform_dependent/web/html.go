package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"st-go/platform_neutral/api"
)

type htmlOverlay struct {
	ID       uint64       `json:"id"`
	X        int          `json:"x"`
	Y        int          `json:"y"`
	Width    int          `json:"width"`
	Height   int          `json:"height"`
	View     api.HTMLView `json:"view"`
	Document string       `json:"document"` // immutable resource ID; View never embeds its body
}

func (*Driver) SupportsHTML() bool { return true }

func (s *surface) PresentHTML(_ uint64, view api.HTMLView) bool {
	d := s.driver
	d.mu.Lock()
	previous, id := s.html.Document, s.htmlID
	d.mu.Unlock()
	if previous != view.Document {
		// Large immutable documents are hashed once, outside the transport
		// lock so input/ACK processing can continue during publication.
		id = htmlDocumentID(view.Document)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed || s.hidden {
		return false
	}
	if s.html != view {
		s.htmlID = id
		if s.scene != nil {
			s.scene.hidden = true
			s.scene.dirty = true
		}
		s.html = view
		s.media = api.MediaView{}
		d.htmlVersion++
		d.notify()
	}
	return true
}

// Snapshot all visible panes. Widget surfaces already own geometry, tab
// visibility, close and resize, so HTML follows the same lifecycle as pixels.
func (d *Driver) htmlSnapshot(previous uint64) (uint64, *serverMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if previous == d.htmlVersion {
		return previous, nil
	}
	m := &serverMessage{Type: "html", Width: d.width, Height: d.height, Overlays: []htmlOverlay{}}
	for _, s := range d.surfaces {
		if !s.closed && !s.hidden && s.media.ID != 0 {
			m.Media = append(m.Media, mediaOverlay{s.id, s.bounds.Min.X, s.bounds.Min.Y, s.bounds.Dx(), s.bounds.Dy(), s.media})
		}
		if !s.closed && !s.hidden && s.html.Document != nil {
			view := s.html
			view.Document = nil
			m.Overlays = append(m.Overlays, htmlOverlay{s.id, s.bounds.Min.X, s.bounds.Min.Y, s.bounds.Dx(), s.bounds.Dy(), view, s.htmlID})
		}
	}
	return d.htmlVersion, m
}

func htmlDocumentID(doc *api.HTMLDocument) string {
	if doc == nil {
		return ""
	}
	h := sha256.New()
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(doc.HTML)))
	h.Write(size[:])
	for _, text := range []string{doc.HTML, doc.CSS} {
		for len(text) > 0 {
			n := min(len(text), 32<<10)
			io.WriteString(h, text[:n])
			text = text[n:]
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Resources live as long as a retained surface references them, including
// hidden tabs. Content IDs survive reconnect; replacement cannot alias a URL.
// Encoding and network writes are outside the driver/terminal locks.
func (d *Driver) serveHTML(w http.ResponseWriter, r *http.Request, resource string) {
	id := strings.TrimPrefix(resource, "html/")
	var doc *api.HTMLDocument
	d.mu.Lock()
	for _, s := range d.surfaces {
		if !s.closed && s.htmlID == id && s.html.Document != nil {
			doc = s.html.Document
			break
		}
	}
	d.mu.Unlock()
	if doc == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", `W/"`+id+`"`)
	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// The common revalidation path avoids encoding a large body at all;
	// ServeContent handles other conditional/range requests below.
	if r.Header.Get("If-None-Match") == w.Header().Get("ETag") {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	data, err := json.Marshal(doc)
	if err != nil {
		http.Error(w, "document encoding failed", http.StatusInternalServerError)
		return
	}
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		var compressed bytes.Buffer
		z, _ := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
		z.Write(data)
		z.Close()
		data = compressed.Bytes()
		w.Header().Set("Content-Encoding", "gzip")
	}
	http.ServeContent(w, r, id+".json", time.Time{}, bytes.NewReader(data))
}

func acceptsGzip(header string) bool {
	for _, encoding := range strings.Split(header, ",") {
		parts := strings.Split(encoding, ";")
		if strings.TrimSpace(parts[0]) != "gzip" {
			continue
		}
		for _, param := range parts[1:] {
			if q, ok := strings.CutPrefix(strings.TrimSpace(param), "q="); ok {
				quality, err := strconv.ParseFloat(q, 64)
				if err != nil || quality <= 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}
