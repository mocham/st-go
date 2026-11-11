package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"st-go/platform_neutral/api"
	"st-go/tui/preview"
)

type webPreviewJob struct {
	ctx        context.Context
	generation uint64
	path       string
}

type webPreviewResult struct {
	generation uint64
	data       []byte
	err        error
}

type webPreviewState struct {
	active, html bool
	generation   uint64
	cancel       context.CancelFunc
	stop         context.CancelFunc
	jobs         chan webPreviewJob
	results      chan webPreviewResult
}

func (b *Browser) clearWebPreview() {
	s := &b.webPreview
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.generation++
	if s.active {
		b.dcs("region reset")
		s.active = false
	}
}

func (b *Browser) closeWebPreview() {
	b.clearWebPreview()
	if b.webPreview.stop != nil {
		b.webPreview.stop()
		b.webPreview.stop = nil
	}
}

func (b *Browser) webPreviewRect() {
	b.dcs(fmt.Sprintf("region overlay 1 3 %d %d", b.previewW, max(1, b.rows-3)))
	b.webPreview.active = true
}

func (b *Browser) renderWebPreview(path string) bool {
	html := preview.WebSupported(path)
	if (!html && !b.isGraphic(path)) || !isRegularFile(path) {
		return false
	}
	b.clearPreview()
	b.previewPageTotal = 0
	b.webPreview.html = html
	if html {
		b.drawPreviewMessage("HTML  "+displayText(baseName(path)), "Loading preview…", "")
		b.queueWebPreview(path)
		return true
	}
	// A stable link preserves arbitrary filenames and extensions. Unlike the
	// native synchronous open path, asynchronous loaders must never follow a
	// mutable `content` link that may already point at the next selection.
	link, err := b.webMediaLink(path)
	if err != nil {
		b.drawPreviewMessage("PREVIEW ERROR", displayText(baseName(path)), err.Error())
		return true
	}
	b.drawPreviewTitle("PREVIEW  " + displayText(baseName(path)))
	b.webPreviewRect()
	b.dcs(fmt.Sprintf("region secondary open '%s' fit-contain page %d", strings.ReplaceAll(link, "'", "'\"'\"'"), b.page))
	return true
}

func (b *Browser) webMediaLink(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	link := filepath.Join(b.cacheDir, fmt.Sprintf("web-%x%s", sha256.Sum256([]byte(absolute)), filepath.Ext(path)))
	if err := os.Symlink(absolute, link); err != nil && !os.IsExist(err) {
		return "", err
	}
	return link, nil
}

func (b *Browser) queueWebPreview(path string) {
	s := &b.webPreview
	if s.jobs == nil {
		s.jobs = make(chan webPreviewJob, 1)
		s.results = make(chan webPreviewResult, 1)
		var ctx context.Context
		ctx, s.stop = context.WithCancel(context.Background())
		go webPreviewWorker(ctx, s.jobs, s.results, renderWebHTML)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	job := webPreviewJob{ctx: ctx, generation: s.generation, path: path}
	// One running conversion and one pending request. Rapid navigation replaces
	// pending work; only the event loop ever publishes output or changes the UI.
	select {
	case <-s.jobs:
	default:
	}
	s.jobs <- job
}

func webPreviewWorker(ctx context.Context, jobs <-chan webPreviewJob, results chan<- webPreviewResult, render func(context.Context, string) ([]byte, error)) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobs:
			if ctx.Err() != nil || job.ctx.Err() != nil {
				continue
			}
			data, err := render(job.ctx, job.path)
			if job.ctx.Err() != nil {
				continue
			}
			select {
			case results <- webPreviewResult{job.generation, data, err}:
			case <-job.ctx.Done():
			case <-ctx.Done():
				return
			}
		}
	}
}

func renderWebHTML(ctx context.Context, path string) (data []byte, err error) {
	defer func() {
		if problem := recover(); problem != nil {
			data, err = nil, fmt.Errorf("HTML preview: %v", problem)
		}
	}()
	source, err := preview.LoadWeb(path, func(name string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		f, err := os.Open(name)
		if err != nil {
			return "", err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Size() > maxPreviewDocumentSize {
			return "", fmt.Errorf("document is not a bounded regular file")
		}
		text, err := io.ReadAll(io.LimitReader(f, maxPreviewDocumentSize+1))
		if len(text) > maxPreviewDocumentSize {
			return "", fmt.Errorf("document is too large to preview")
		}
		return string(text), err
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, _, err := preview.RenderHTML(source)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return preview.EncodeHTML(api.HTMLDocument{HTML: body, CSS: preview.HTMLStyle})
}

func (b *Browser) pollWebPreview() {
	select {
	case result := <-b.webPreview.results:
		if result.generation != b.webPreview.generation || b.cleaned || b.promptActive || b.manualActive || b.compact {
			return
		}
		b.paintStop()
		if result.err == nil {
			b.webPreviewRect()
			result.err = preview.SendHTML(result.data, func(command string) error {
				b.dcs(command)
				return nil
			})
		}
		if result.err != nil {
			b.clearPreview()
			b.drawPreviewMessage("RENDER ERROR", shorten(result.err.Error(), b.previewW-4), "")
		}
		b.paintResume()
		b.flush()
	default:
	}
}
