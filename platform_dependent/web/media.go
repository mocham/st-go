package web

import (
	"bytes"
	"context"
	"fmt"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"net/http"
	"strconv"
	"strings"
	"time"

	"st-go/limits"
	"st-go/platform_neutral/api"
)

type mediaResource struct {
	watch  func(api.MediaStatus)
	ready  chan struct{}
	cancel context.CancelFunc
	data   []byte
	mime   string
	err    error
}

func (d *Driver) WatchMedia(id uint64, done func(api.MediaStatus)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r := d.media[id]; r != nil {
		r.watch = done
	}
}
func (d *Driver) mediaStatus(status api.MediaStatus) {
	if status.Page < 0 || status.Pages < 0 || status.Pages > 100000 || status.Position < 0 || status.Position > 1000000 || len(status.Error) > 4096 {
		return
	}
	d.mu.Lock()
	var watch func(api.MediaStatus)
	if r := d.media[status.ID]; r != nil {
		watch = r.watch
	}
	d.mu.Unlock()
	if watch != nil {
		watch(status)
	}
}

type mediaJob struct {
	resource *mediaResource
	source   api.MediaSource
	ctx      context.Context
}

func (d *Driver) RegisterMedia(source api.MediaSource) uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	select {
	case <-d.done:
		return 0
	default:
	}
	if len(d.media) >= 64 || len(source.Data) > limits.FileBytes {
		return 0
	}
	if d.media == nil {
		d.media = make(map[uint64]*mediaResource)
		d.mediaJobs = make(chan mediaJob, 32)
		for range 2 {
			go d.mediaWorker()
		}
	}
	d.nextMediaID++
	id := d.nextMediaID
	ctx, cancel := context.WithCancel(context.Background())
	r := &mediaResource{ready: make(chan struct{}), cancel: cancel}
	d.media[id] = r
	// The producer transfers ownership of encoded bytes. File reads and format
	// conversion are bounded workers; a saturated queue fails without blocking.
	select {
	case d.mediaJobs <- mediaJob{r, source, ctx}:
	default:
		r.err = fmt.Errorf("media loader busy")
		close(r.ready)
	}
	return id
}
func (d *Driver) ReleaseMedia(id uint64) {
	d.mu.Lock()
	if r := d.media[id]; r != nil {
		r.cancel()
		d.mediaBytes -= len(r.data)
		delete(d.media, id)
	}
	d.mu.Unlock()
}
func (d *Driver) mediaWorker() {
	for {
		select {
		case <-d.done:
			return
		case job := <-d.mediaJobs:
			r := job.resource
			if job.ctx.Err() != nil {
				r.err = job.ctx.Err()
				close(r.ready)
				continue
			}
			data := job.source.Data
			var err error
			if data == nil {
				data, err = limits.ReadFile(job.source.Path)
			}
			if len(data) > limits.FileBytes {
				err = fmt.Errorf("media exceeds file budget")
			}
			mime := http.DetectContentType(data)
			if bytes.HasPrefix(data, []byte("%PDF-")) {
				mime = "application/pdf"
			}
			if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
				mime = "image/webp"
			}
			// Header validation bounds browser allocations without decoding pixels.
			if err == nil && strings.HasPrefix(mime, "image/") {
				config, _, configErr := image.DecodeConfig(bytes.NewReader(data))
				err = configErr
				if err == nil {
					if _, ok := limits.ImageRGBA("browser image", config.Width, config.Height); !ok {
						err = fmt.Errorf("image dimensions exceed budget")
					}
				}
			}
			if err == nil && mime != "application/pdf" && !strings.HasPrefix(mime, "image/") {
				err = fmt.Errorf("unsupported media format")
			}
			// Browser-inconsistent formats use pure-Go conversion on a worker.
			if err == nil && (strings.Contains(mime, "tiff") || strings.Contains(mime, "bmp")) {
				var config image.Config
				config, _, err = image.DecodeConfig(bytes.NewReader(data))
				if err == nil {
					if _, ok := limits.ImageRGBA("media", config.Width, config.Height); !ok {
						err = fmt.Errorf("image dimensions exceed budget")
					}
				}
				if err == nil {
					var img image.Image
					img, _, err = image.Decode(bytes.NewReader(data))
					if err == nil {
						var out limits.Buffer
						err = png.Encode(&out, img)
						data = out.Bytes()
						mime = "image/png"
					}
				}
			}
			d.mu.Lock()
			if job.ctx.Err() != nil {
				data = nil
				err = job.ctx.Err()
			}
			if len(data) > 128<<20-d.mediaBytes {
				data = nil
				err = fmt.Errorf("encoded media budget exhausted")
			}
			r.data, r.mime, r.err = data, mime, err
			d.mediaBytes += len(data)
			close(r.ready)
			d.mu.Unlock()
		}
	}
}
func (d *Driver) serveMedia(w http.ResponseWriter, r *http.Request, path string) {
	id, err := strconv.ParseUint(strings.TrimPrefix(path, "media/"), 10, 64)
	d.mu.Lock()
	resource := d.media[id]
	d.mu.Unlock()
	if err != nil || resource == nil {
		http.NotFound(w, r)
		return
	}
	select {
	case <-resource.ready:
	case <-r.Context().Done():
		return
	case <-d.done:
		return
	}
	if resource.err != nil {
		http.Error(w, resource.err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", resource.mime)
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	http.ServeContent(w, r, strconv.FormatUint(id, 10), time.Time{}, bytes.NewReader(resource.data))
}

type mediaOverlay struct {
	ID     uint64        `json:"id"`
	X      int           `json:"x"`
	Y      int           `json:"y"`
	Width  int           `json:"width"`
	Height int           `json:"height"`
	View   api.MediaView `json:"view"`
}

func (s *surface) PresentMedia(_ uint64, view api.MediaView) bool {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed || s.hidden {
		return false
	}
	if s.scene != nil {
		s.scene.hidden = true
		s.scene.dirty = true
	}
	if s.media != view {
		s.media = view
		s.html = api.HTMLView{}
		s.pixels = nil
		d.htmlVersion++
		d.notify()
	}
	return true
}
