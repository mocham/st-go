package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"st-go/platform_neutral/api"
	"st-go/semhtml"
)

type resourceJob struct {
	id            uint64
	ctx           context.Context
	remote        remote
	key, name     string
	html          bool
	width, height int
}
type resourceResult struct {
	id        uint64
	key, path string
	err       error
}
type cacheEntry struct {
	path string
	size int64
	used time.Time
}
type resourceCache struct {
	directory string
	entries   map[string]cacheEntry // one worker owns entries
	bytes     int64
	mu        sync.Mutex
	pins      map[string]bool
	jobs      chan resourceJob
	results   chan resourceResult
	done      chan struct{}
}

func newCache(ctx context.Context) (*resourceCache, error) {
	dir, err := os.MkdirTemp("", "st-client-")
	if err != nil {
		return nil, err
	}
	if strings.IndexFunc(dir, unicode.IsControl) >= 0 {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("temporary directory contains terminal control characters")
	}
	c := &resourceCache{directory: dir, entries: make(map[string]cacheEntry), pins: make(map[string]bool), jobs: make(chan resourceJob, 64), results: make(chan resourceResult, 64), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-c.jobs:
				path, err := c.load(job)
				select {
				case c.results <- resourceResult{job.id, job.key, path, err}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return c, nil
}

func (c *resourceCache) close()                   { <-c.done; os.RemoveAll(c.directory) }
func (c *resourceCache) pin(keys map[string]bool) { c.mu.Lock(); c.pins = keys; c.mu.Unlock() }

func (c *resourceCache) lookup(key string) (string, bool) {
	e, ok := c.entries[key]
	if ok {
		e.used = time.Now()
		c.entries[key] = e
	}
	return e.path, ok
}

func (c *resourceCache) store(key, extension string, data []byte) (string, error) {
	if len(data) > maxResource {
		return "", fmt.Errorf("resource exceeds 64 MiB")
	}
	for len(c.entries) >= 32 || c.bytes+int64(len(data)) > 128<<20 {
		oldest := ""
		var stamp time.Time
		c.mu.Lock()
		for k, e := range c.entries {
			if !c.pins[k] && (oldest == "" || e.used.Before(stamp)) {
				oldest, stamp = k, e.used
			}
		}
		c.mu.Unlock()
		if oldest == "" {
			return "", fmt.Errorf("active resources exceed cache budget")
		}
		e := c.entries[oldest]
		os.Remove(e.path)
		c.bytes -= e.size
		delete(c.entries, oldest)
	}
	name := filepath.Join(c.directory, fmt.Sprintf("%x%s", sha256.Sum256([]byte(key)), extension))
	if err := os.WriteFile(name, data, 0600); err != nil {
		return "", err
	}
	c.entries[key] = cacheEntry{name, int64(len(data)), time.Now()}
	c.bytes += int64(len(data))
	return name, nil
}

func (c *resourceCache) load(job resourceJob) (path string, err error) {
	defer func() {
		if p := recover(); p != nil {
			path = ""
			err = fmt.Errorf("document conversion failed: %v", p)
		}
	}()
	if err = job.ctx.Err(); err != nil {
		return "", err
	}
	if path, ok := c.lookup(job.key); ok {
		return path, nil
	}
	url := job.remote.resource(job.name)
	var data []byte
	if cached, ok := c.lookup(url); ok {
		data, err = os.ReadFile(cached)
	} else {
		data, err = job.remote.request(job.ctx, "GET", url, nil, maxResource)
	}
	if err != nil {
		return "", err
	}
	if !job.html {
		ext := ""
		switch {
		case strings.HasPrefix(string(data[:min(5, len(data))]), "%PDF-"):
			ext = ".pdf"
		case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
			ext = ".webp"
		default:
			switch http.DetectContentType(data) {
			case "image/png":
				ext = ".png"
			case "image/jpeg":
				ext = ".jpg"
			case "image/gif":
				ext = ".gif"
			}
		}
		if ext == "" {
			return "", fmt.Errorf("unsupported media response")
		}
		return c.store(job.key, ext, data)
	}
	if _, ok := c.lookup(url); !ok {
		if _, err = c.store(url, ".json", data); err != nil {
			return "", err
		}
	}
	var doc api.HTMLDocument
	if err = json.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("invalid HTML resource: %w", err)
	}
	// The original HTML/CSS is cached unchanged. Native conversion consumes the
	// existing semantic subset; it never reconstructs LaTeX from browser pixels.
	if len(doc.HTML) > 8<<20 {
		return "", fmt.Errorf("semantic HTML exceeds the 8 MiB local conversion limit")
	}
	if err = job.ctx.Err(); err != nil {
		return "", err
	}
	data, err = semhtml.NewRenderer().RenderPDFWithOptions(semhtml.Document{HTML: doc.HTML}, semhtml.PDFOptions{
		PageWidth: max(60, float64(job.width)*1.8), PageHeight: max(80, float64(job.height)*3.8),
	})
	if err != nil {
		return "", err
	}
	if err = job.ctx.Err(); err != nil {
		return "", err
	}
	return c.store(job.key, ".pdf", data)
}

func makeResource(r *remote, name string, html bool, width, height int) resourceJob {
	key := r.resource(name)
	if html {
		key += fmt.Sprintf("#native-v1-%dx%d", width, height)
	} else {
		// Numeric media IDs can be reused after a service restart. HTML content
		// hashes remain safe to reuse across attachments.
		key += fmt.Sprintf("#attachment-%d", r.generation)
	}
	return resourceJob{remote: *r, key: key, name: name, html: html, width: width, height: height}
}
