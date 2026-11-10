package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"st-go/limits"
	"st-go/tui/outline"
	"st-go/tui/preview"
)

type previewOptions struct {
	width      int
	background string
	gap        time.Duration
}

func defaultPreviewOptions() previewOptions {
	return previewOptions{preview.DefaultWidthPercent, "#f7f5ef", 2100 * time.Millisecond}
}

type previewRequest struct {
	id         uint64
	path       string
	line       int
	noMove     bool
	cursorOnly bool // explicit navigation ignores a document's %cursor marker
	immediate  bool // explicit toggles bypass the automatic save-refresh gap
	navigation uint64
	overrides  map[string]string
	reuse      bool
	hash       [sha256.Size]byte
}
type previewResult struct {
	request    previewRequest
	source     *preview.Snapshot
	pdf        []byte
	mathErrors int
	err        error
}
type previewEvent struct {
	kind              string
	value, cols, rows int
	token             int
	secondary         bool
}

type editorPreview struct {
	toc                                            *editorTOC
	options                                        previewOptions
	out                                            io.Writer
	path                                           string
	serial, navigation                             uint64
	split, ready, unsupported, closed, busy, clear bool
	deadline, lastAttempt, lastPublished           time.Time
	pending                                        *previewRequest
	completed                                      *previewResult
	source                                         *preview.Snapshot
	published                                      bool
	directory, pdfPath                             string
	commands                                       []string
	results                                        chan previewResult
	stop                                           chan struct{}
	render                                         func(previewRequest, <-chan struct{}) previewResult
}

func newEditorPreview(options previewOptions, out io.Writer) *editorPreview {
	return &editorPreview{options: options, out: out, results: make(chan previewResult, 1), stop: make(chan struct{}), render: renderPreview}
}

func previewText(b *Buffer) string {
	text := string(b.text)
	if b.meta.FinalEOL && !b.placeholder {
		text += "\n"
	}
	return text
}

func renderPreview(request previewRequest, stop <-chan struct{}) (result previewResult) {
	result.request = request
	defer func() {
		if problem := recover(); problem != nil {
			result.err = fmt.Errorf("preview: %v", problem)
		}
	}()
	read := func(path string) (string, error) {
		select {
		case <-stop:
			return "", fmt.Errorf("preview cancelled")
		default:
		}
		if text, ok := request.overrides[path]; ok {
			return text, nil
		}
		b, err := loadBuffer(path)
		if err != nil {
			return "", err
		}
		if !b.meta.Existed {
			return "", os.ErrNotExist
		}
		return previewText(b), nil
	}
	result.source, result.err = preview.Load(request.path, read)
	if result.err != nil {
		return
	}
	select {
	case <-stop:
		result.err = fmt.Errorf("preview cancelled")
		return
	default:
	}
	if request.reuse && request.hash == result.source.Hash {
		return
	}
	result.pdf, result.mathErrors, result.err = preview.Render(result.source)
	return
}

func (p *editorPreview) command(command string) error {
	return writeAll(p.out, []byte("\x1bP"+command+"\x1b\\"))
}
func quotePreview(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func (p *editorPreview) queue(e *Editor, noMove bool, overrides map[string]string) {
	p.serial++
	request := &previewRequest{id: p.serial, path: p.path, line: e.buffer().row(e.pos()) + 1, noMove: noMove, navigation: p.navigation, overrides: overrides, reuse: p.published}
	if p.source != nil {
		request.hash = p.source.Hash
	}
	p.pending = request
	p.completed = nil
}

func (p *editorPreview) observe(e *Editor) {
	path := e.buffer().meta.Path
	if !preview.Supported(path) {
		path = ""
	}
	if path == p.path {
		return
	}
	p.serial++
	p.path = path
	p.pending = nil
	p.completed = nil
	p.source = nil
	p.published = false
	p.commands = nil
	p.clear = true
	p.lastAttempt = time.Time{}
	p.lastPublished = time.Time{}
	if path != "" {
		var overrides map[string]string
		b := e.buffer()
		if !b.dirty() {
			overrides = map[string]string{path: previewText(b)}
		} else if !b.meta.Existed {
			overrides = map[string]string{path: ""}
		}
		p.queue(e, false, overrides)
	}
}

// Called only after a successful editor write/reload, never by a stat timer.
// Saves of an open included file also refresh its active parent preview.
func (p *editorPreview) saved(e *Editor, path string, text *string, noMove bool) {
	if p == nil || p.closed || p.unsupported || len(e.tabs) == 0 {
		return
	}
	p.observe(e)
	if p.path == "" || path != p.path && p.source != nil && !p.source.HasFile(path) {
		return
	}
	var overrides map[string]string
	if text != nil {
		overrides = map[string]string{path: *text}
	}
	p.queue(e, noMove, overrides)
}

func (p *editorPreview) toggle(e *Editor, unsaved bool) error {
	if p == nil || p.closed {
		return fmt.Errorf("preview is disabled (omit --no-preview)")
	}
	if p.unsupported {
		return fmt.Errorf("preview unavailable: terminal does not support display regions")
	}
	p.observe(e)
	if p.path == "" {
		return fmt.Errorf("preview requires a named LaTeX/Markdown buffer")
	}
	if err := p.restorePreview(); err != nil {
		return err
	}
	var overrides map[string]string
	if unsaved {
		// The renderer never reads mutable Editor/Buffer state. Capture all named
		// open buffers so an unsaved root, fragment, or newly added include is
		// interpreted consistently, including files reached through %main.
		overrides = make(map[string]string)
		bytes := 0
		for _, b := range e.buffers {
			if b.meta.Path == "" {
				continue
			}
			text := previewText(b)
			bytes += len(text)
			if bytes > limits.FileBytes {
				return fmt.Errorf("unsaved preview snapshots exceed resource limit")
			}
			overrides[b.meta.Path] = text
		}
	}
	p.navigation++
	p.commands = nil
	p.queue(e, false, overrides)
	p.pending.cursorOnly, p.pending.immediate = true, true
	if unsaved {
		e.message = "Preview: buffer snapshot (not written)"
	}
	return nil
}

func (e *Editor) saveBuffer(b *Buffer, force, noMove bool) error {
	if err := b.save(force); err != nil {
		return err
	}
	if e.preview != nil {
		text := previewText(b)
		e.preview.saved(e, b.meta.Path, &text, noMove)
	}
	return nil
}

func (p *editorPreview) tick(e *Editor, now time.Time) error {
	if p == nil || p.closed || len(e.tabs) == 0 {
		return nil
	}
	p.observe(e)
	select {
	case result := <-p.results:
		p.busy = false
		if result.request.id == p.serial && result.request.path == p.path {
			if result.err != nil {
				e.error(fmt.Errorf("preview: %w", result.err))
			} else {
				p.completed = &result
			}
		}
	default:
	}
	if p.path == "" {
		if p.toc != nil {
			p.toc.active, p.toc.sent = false, false
		}
		if p.split {
			if err := p.command("region reset"); err != nil {
				return err
			}
			p.split, p.ready = false, false
			e.dirty, e.fullRedraw = true, true
		}
		return nil
	}
	if p.unsupported {
		return nil
	}
	if !p.split {
		// Don't establish an unusably narrow main pane in a tiny terminal.
		if e.cols*(100-p.options.width)/100 < 8 || e.rows < 3 {
			return nil
		}
		if err := p.command(fmt.Sprintf("region split %d%%; region secondary scroll report; region query", p.options.width)); err != nil {
			return err
		}
		p.split = true
		p.ready = false
		p.deadline = now.Add(2 * time.Second)
	}
	if !p.ready {
		if !now.Before(p.deadline) {
			p.unsupported = true
			p.split = false
			p.pending = nil
			p.completed = nil
			if err := p.command("region reset"); err != nil {
				return err
			}
			e.error(fmt.Errorf("preview unavailable: terminal did not confirm display regions (use st-go); editing remains available"))
		}
		return nil
	}
	if p.clear {
		if err := p.command("region secondary clear"); err != nil {
			return err
		}
		p.clear = false
		if p.toc != nil {
			p.toc.sent = false
		}
	}
	if p.toc != nil && p.toc.active {
		return p.drawContents(e)
	}
	if result := p.completed; result != nil && (result.request.immediate || p.lastPublished.IsZero() || now.Sub(p.lastPublished) >= p.options.gap) {
		if len(result.pdf) > 0 {
			if err := p.publish(result.pdf); err != nil {
				e.error(fmt.Errorf("preview: %w", err))
				p.completed = nil
				return nil
			}
			if err := p.command("region secondary open " + quotePreview(p.pdfPath) + " background " + quotePreview(p.options.background) + " fit-width continuous"); err != nil {
				return err
			}
			p.lastPublished = now
		}
		p.source = result.source
		p.published = true
		if !result.request.noMove && result.request.navigation == p.navigation {
			position := result.source.Position(result.request.path, result.request.line)
			if result.source.CursorLine > 0 && !result.request.cursorOnly {
				position = result.source.ExpandedPosition(result.source.CursorLine)
			}
			if err := p.command(fmt.Sprintf("region secondary position %d", position)); err != nil {
				return err
			}
		}
		if result.mathErrors > 0 {
			e.message = fmt.Sprintf("Preview: %d mathematical expressions could not be rendered", result.mathErrors)
			e.dirty = true
		}
		p.completed = nil
	}
	for _, command := range p.commands {
		if err := p.command(command); err != nil {
			return err
		}
	}
	p.commands = nil
	if p.pending != nil && !p.busy && (p.pending.immediate || (p.lastAttempt.IsZero() || now.Sub(p.lastAttempt) >= p.options.gap) && (p.lastPublished.IsZero() || now.Sub(p.lastPublished) >= p.options.gap)) {
		request := *p.pending
		p.pending = nil
		p.busy = true
		p.lastAttempt = now
		// An earlier in-flight revision may have become the current published
		// PDF since this request was queued. Reuse only that exact document.
		request.reuse = p.published
		if p.source != nil {
			request.hash = p.source.Hash
		}
		render, stop, results := p.render, p.stop, p.results
		go func() {
			result := render(request, stop)
			result.request.overrides = nil
			select {
			case results <- result:
			case <-stop:
			}
		}()
	}
	return nil
}

func (p *editorPreview) publish(pdf []byte) error {
	if len(pdf) > limits.FileBytes {
		return fmt.Errorf("preview PDF exceeds resource limit")
	}
	if p.directory == "" {
		directory, err := os.MkdirTemp("", "govim-preview-")
		if err != nil {
			return err
		}
		p.directory = directory
		p.pdfPath = filepath.Join(directory, "preview.pdf")
	}
	next := p.pdfPath + ".next"
	defer os.Remove(next)
	if err := os.WriteFile(next, pdf, 0600); err != nil {
		return err
	}
	return os.Rename(next, p.pdfPath)
}

func (p *editorPreview) event(e *Editor, event previewEvent) {
	if p == nil || p.closed {
		return
	}
	if event.kind == "activate" {
		p.activateContents(e, event)
		return
	}
	if event.kind == "layout" {
		if p.split && !p.unsupported && event.secondary && event.cols >= 8 && event.rows >= 2 {
			p.ready = true
			if e.cols != event.cols || e.rows != event.rows {
				e.cols, e.rows = event.cols, event.rows
				e.dirty, e.fullRedraw = true, true
			}
		}
		return
	}
	if p.toc != nil && p.toc.active {
		return
	}
	if !p.published || !p.ready || p.source == nil {
		return
	}
	p.navigation++
	switch event.kind {
	case "seek":
		p.commands = append(p.commands, fmt.Sprintf("region secondary position %d", p.source.Position(p.path, event.value)))
	case "scroll":
		p.commands = append(p.commands, fmt.Sprintf("region secondary scroll %d", event.value))
	}
	if len(p.commands) > 128 {
		p.commands = p.commands[len(p.commands)-128:]
	}
}

func (p *editorPreview) close() error {
	if p == nil || p.closed {
		return nil
	}
	p.closed = true
	close(p.stop)
	p.pending, p.completed, p.source = nil, nil, nil
	p.commands = nil
	var err error
	if p.split {
		err = p.command("region reset")
	}
	if p.directory != "" {
		if removeErr := os.RemoveAll(p.directory); err == nil {
			err = removeErr
		}
	}
	return err
}

func parsePreviewEvent(control string) *previewEvent {
	fields := strings.Fields(control)
	if len(fields) == 6 && fields[0] == "region" && fields[1] == "secondary" && fields[2] == "event" && fields[3] == "activate" {
		token, e1 := strconv.Atoi(fields[4])
		item, e2 := strconv.Atoi(fields[5])
		if e1 == nil && e2 == nil && token >= 1 && token <= 1<<30 && item >= 1 && item <= outline.MaxEntries {
			return &previewEvent{kind: "activate", token: token, value: item}
		}
		return nil
	}
	if len(fields) == 12 && fields[0] == "region" && fields[1] == "layout" && fields[2] == "main" && fields[7] == "secondary" {
		values := make([]int, 8)
		for j, i := range []int{3, 4, 5, 6, 8, 9, 10, 11} {
			n, err := strconv.Atoi(fields[i])
			if err != nil || n < 1 || n > 100000 {
				return nil
			}
			values[j] = n
		}
		if values[0] != 1 || values[1] != 1 || values[4] != values[2]+1 || values[5] != 1 || values[3] != values[7] {
			return nil
		}
		return &previewEvent{kind: "layout", cols: min(values[2], 1000), rows: min(values[3], 500), secondary: true}
	}
	if len(fields) == 5 && fields[0] == "region" && fields[1] == "secondary" && fields[2] == "event" && (fields[3] == "seek" || fields[3] == "scroll") {
		value, err := strconv.Atoi(fields[4])
		if err == nil && value >= -limits.MaxBytes && value <= limits.MaxBytes && (fields[3] != "seek" || value > 0) {
			return &previewEvent{kind: fields[3], value: value}
		}
	}
	return nil
}
