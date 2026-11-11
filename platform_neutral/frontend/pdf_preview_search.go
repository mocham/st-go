package frontend

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"strings"
	"sync"
	"unicode/utf8"

	"st-go/limits"
	"st-go/pdfmeta"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

// Terminal.mu -> mu. The worker never takes Terminal.mu, writes to the PTY, or
// touches the image atlas. It publishes immutable bounded match/size snapshots.
type pdfSearchState struct {
	cancel     func()
	resource   uint64
	mu         sync.Mutex
	generation uint64
	token      int
	jobs       chan pdfSearchJob
	data       []byte
	reply      string
	result     *pdfPreviewMatches
}

type pdfSearchJob struct {
	generation uint64
	request    term.PDFSearchRequest
}

type pdfPreviewMatches struct {
	digest  [32]byte
	matches *pdfMatches
	sizes   map[int]pdfPageSize
}

func (t *Terminal) SearchPDF(request term.PDFSearchRequest) error {
	s := &t.pdfSearch
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil && (request.Token == 0 || request.Path != "") {
		s.cancel()
		s.cancel = nil
		t.host.(api.MediaRegistry).ReleaseMedia(s.resource)
		s.resource = 0
	}
	if request.Token == 0 {
		s.generation++
		s.token, s.data, s.result, s.reply = 0, nil, nil, ""
		return nil
	}
	if request.Token < 1 || request.Token > 1<<30 || request.Offset < 0 || request.Offset > pdfmeta.MaxBytes {
		return fmt.Errorf("invalid PDF search request")
	}
	if request.Path == "" {
		if request.Token == s.token && s.data != nil {
			s.chunk(request.Offset)
		}
		return nil
	}
	if request.Text == "" || len(request.Text) > limits.PreviewSearchBytes || !utf8.ValidString(request.Text) || strings.ContainsRune(request.Text, 0) {
		return fmt.Errorf("invalid PDF search query")
	}
	s.generation++
	s.token, s.data, s.result, s.reply = request.Token, nil, nil, ""
	if t.scene() != nil {
		registry := t.host.(api.MediaRegistry)
		s.resource = registry.RegisterMedia(api.MediaSource{Path: request.Path})
		generation := s.generation
		s.cancel = t.host.(api.DocumentHost).QueryMedia(s.resource, api.DocumentQuery{Kind: "search", Text: request.Text}, func(data []byte, err error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if t.closed() || s.generation != generation {
				return
			}
			if err != nil {
				data, _ = json.Marshal(pdfmeta.Search{Error: err.Error()})
			}
			s.data = data
			s.chunk(0)
		})
		return nil
	}
	if s.jobs == nil {
		s.jobs = make(chan pdfSearchJob, 1)
		if t.done == nil {
			t.done = make(chan struct{})
		}
		go t.pdfSearchWorker(s.jobs, t.done)
	}
	select {
	case <-s.jobs:
	default:
	}
	s.jobs <- pdfSearchJob{s.generation, request}
	return nil
}

func (s *pdfSearchState) chunk(offset int) {
	if offset > len(s.data) {
		return
	}
	end := min(len(s.data), offset+pdfmeta.ChunkBytes)
	next := end
	if end == len(s.data) {
		next = -1
	}
	s.reply = fmt.Sprintf("\x1bPpdfsearch data %d %d %d %s\x1b\\", s.token, offset, next, base64.StdEncoding.EncodeToString(s.data[offset:end]))
}

func (t *Terminal) pdfSearchWorker(jobs <-chan pdfSearchJob, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case job := <-jobs:
			s := &t.pdfSearch
			current := func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				return !t.closed() && job.generation == s.generation
			}
			if !current() {
				continue
			}
			result, metadata := searchPDFPreview(job.request, current)
			data, err := json.Marshal(metadata)
			if err != nil || len(data) > pdfmeta.MaxBytes {
				data = []byte(`{"error":"PDF search result exceeds the preview budget"}`)
				result = nil
			}
			s.mu.Lock()
			if !t.closed() && job.generation == s.generation {
				s.result, s.data = result, data
				s.chunk(0)
			}
			s.mu.Unlock()
		}
	}
}

func searchPDFPreview(request term.PDFSearchRequest, current func() bool) (*pdfPreviewMatches, pdfmeta.Search) {
	data, err := limits.ReadFile(request.Path)
	if err != nil {
		return nil, pdfmeta.Search{Error: err.Error()}
	}
	document, err := openScrollPDF(data)
	if err != nil {
		return nil, pdfmeta.Search{Error: err.Error()}
	}
	defer document.close()
	matches, _, err := document.matches(100, request.Text, current)
	if err != nil {
		return nil, pdfmeta.Search{Error: err.Error()}
	}
	if matches == nil {
		return nil, pdfmeta.Search{}
	}
	result := &pdfPreviewMatches{digest: sha256.Sum256(data), matches: matches, sizes: make(map[int]pdfPageSize)}
	metadata := pdfmeta.Search{Truncated: matches.truncated}
	for _, hit := range matches.hits {
		metadata.Pages = append(metadata.Pages, hit.page+1)
		if _, ok := result.sizes[hit.page]; !ok {
			size, err := document.pageSize(hit.page)
			if err != nil {
				return nil, pdfmeta.Search{Error: err.Error()}
			}
			result.sizes[hit.page] = size
		}
	}
	return result, metadata
}

func (t *Terminal) flushPDFSearchReply() {
	s := &t.pdfSearch
	s.mu.Lock()
	reply := s.reply
	s.reply = ""
	s.mu.Unlock()
	if reply != "" && t.termCore != nil {
		t.termCore.WriteToTTY([]byte(reply), false)
	}
}

func (t *Terminal) paintPDFPreviewSearch(encoded []byte, page, width, height int, pixels []uint32, opts term.ImageDecodeOptions) {
	if opts.SearchToken == 0 {
		return
	}
	s := &t.pdfSearch
	s.mu.Lock()
	result := s.result
	if s.token != opts.SearchToken {
		result = nil
	}
	s.mu.Unlock()
	if result == nil || sha256.Sum256(encoded) != result.digest {
		return
	}
	size, ok := result.sizes[page]
	if !ok {
		return
	}
	scale := min(float64(width)/size.width, float64(height)/size.height)
	w, h := size.width*scale, size.height*scale
	x, y := (width-min(width, int(math.Ceil(w))))/2, (height-min(height, int(math.Ceil(h))))/2
	viewport := image.Rect(0, 0, width, height)
	for i, hit := range result.matches.hits {
		if hit.page != page {
			continue
		}
		box := image.Rect(x+int(math.Floor(float64(hit.box.Min.X)*w/1e6)), y+int(math.Floor(float64(hit.box.Min.Y)*h/1e6)),
			x+int(math.Ceil(float64(hit.box.Max.X)*w/1e6)), y+int(math.Ceil(float64(hit.box.Max.Y)*h/1e6))).Intersect(viewport)
		tintSearchBox(pixels, width, box, i+1 == opts.SearchMatch)
	}
}
