package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"st-go/limits"
	"st-go/pdfmeta"
)

type backendJobKind uint8

const (
	jobRender backendJobKind = iota
	jobText
	jobTOC
	jobSearch
	jobOpen
)

type backendJob struct {
	kind       backendJobKind
	token      uint64
	page       int
	query      string
	path       string
	background uint32
}

type searchOccurrence struct {
	page, local int
}

type backendResult struct {
	kind        backendJobKind
	token       uint64
	page        int
	pages       int
	path        string
	text        string
	toc         []pdfmeta.Entry
	occurrences []searchOccurrence
	width       int
	height      int
	truncated   bool
	err         error
}

// djvuBackend serializes DjVuLibre access. The library posts asynchronous
// messages into one context, so the worker follows zathura-djvu and drains that
// context between synchronous document, page, text, and outline operations.
type djvuBackend struct {
	directory string

	mu     sync.Mutex
	queue  []backendJob
	closed bool
	wake   chan struct{}
	done   chan struct{}
	exited chan struct{}

	results      chan backendResult
	latestSearch atomic.Uint64
}

func newDjVuBackend(path string, background uint32) (*djvuBackend, int, error) {
	document, err := openDjVu(path, background)
	if err != nil {
		return nil, 0, err
	}
	directory, err := os.MkdirTemp("", "zat-djvu-")
	if err != nil {
		document.close()
		return nil, 0, err
	}
	b := &djvuBackend{
		directory: directory,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
		exited:    make(chan struct{}),
		results:   make(chan backendResult, 16),
	}
	go b.run(document)
	return b, document.pages, nil
}

func (b *djvuBackend) submit(job backendJob) bool {
	if job.kind == jobSearch {
		b.latestSearch.Store(job.token)
	} else {
		// Page rendering and pane updates must not sit behind a document-wide
		// hidden-text scan. The running search notices this between pages.
		b.latestSearch.Store(0)
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return false
	}
	if job.kind == jobOpen {
		// Nothing queued against the old document remains useful.
		b.queue = b.queue[:0]
		b.latestSearch.Store(0)
	} else {
		// Keep only the newest unstarted operation of each kind. Page-repeat and
		// resize bursts therefore cannot grow memory or force stale renders.
		filtered := b.queue[:0]
		for _, queued := range b.queue {
			if queued.kind != job.kind {
				filtered = append(filtered, queued)
			}
		}
		b.queue = filtered
	}
	b.queue = append(b.queue, job)
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return true
}

func (b *djvuBackend) pop() (backendJob, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) == 0 {
		return backendJob{}, false
	}
	job := b.queue[0]
	copy(b.queue, b.queue[1:])
	b.queue = b.queue[:len(b.queue)-1]
	return job, true
}

func (b *djvuBackend) publish(result backendResult) {
	select {
	case b.results <- result:
	case <-b.done:
	}
}

func (b *djvuBackend) run(document *djvuDocument) {
	defer close(b.exited)
	defer func() { document.close() }()
	sequence := uint64(0)
	var rendered []string
	clearRendered := func() {
		for _, path := range rendered {
			_ = os.Remove(path)
		}
		rendered = nil
	}
	defer clearRendered()

	for {
		select {
		case <-b.done:
			return
		case <-b.wake:
		}
		for {
			job, ok := b.pop()
			if !ok {
				break
			}
			result := backendResult{kind: job.kind, token: job.token, page: job.page}
			switch job.kind {
			case jobRender:
				sequence++
				path := filepath.Join(b.directory, fmt.Sprintf("page-%06d-%06d.png", job.page, sequence))
				result.width, result.height, result.err = document.renderPNG(job.page, path)
				if result.err == nil {
					result.path = path
					rendered = append(rendered, path)
					// Keep enough generations for a worker that has accepted but not
					// yet opened the previous terminal request.
					for len(rendered) > 3 {
						_ = os.Remove(rendered[0])
						rendered = rendered[1:]
					}
				}
			case jobText:
				result.text, result.truncated, result.err = document.pageText(job.page)
			case jobTOC:
				result.toc, result.truncated, result.err = document.outline()
			case jobSearch:
				for page := 1; page <= document.pages; page++ {
					if b.latestSearch.Load() != job.token {
						result.err = errSearchCanceled
						break
					}
					text, truncated, err := document.pageText(page)
					if err != nil {
						result.err = err
						break
					}
					if b.latestSearch.Load() != job.token {
						result.err = errSearchCanceled
						break
					}
					result.truncated = result.truncated || truncated
					for index := range textSearchHits(cleanPanelText(text), job.query) {
						if len(result.occurrences) == limits.PreviewSearchMatches {
							result.truncated = true
							break
						}
						result.occurrences = append(result.occurrences, searchOccurrence{page: page, local: index + 1})
					}
					if len(result.occurrences) == limits.PreviewSearchMatches {
						break
					}
				}
			case jobOpen:
				next, err := openDjVu(job.path, job.background)
				if err != nil {
					result.err = err
				} else {
					document.close()
					document = next
					result.pages = next.pages
					result.path = job.path
				}
			}
			b.publish(result)
		}
	}
}

func (b *djvuBackend) close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		close(b.done)
	}
	b.mu.Unlock()
	<-b.exited
	_ = os.RemoveAll(b.directory)
}

var errSearchCanceled = fmt.Errorf("DjVu search canceled")
