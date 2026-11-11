package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"st-go/platform_neutral/api"
	"st-go/tui/preview"
)

type richRenderRequest struct {
	generation uint64
	messageID  string
	text       string
	media      *fileMedia
}

type richRenderResult struct {
	request    richRenderRequest
	pdf        []byte
	html       []byte // JSON, ready for single-command or chunked DCS delivery
	mathErrors int
	err        error
	mediaPath  string
}

type richPreview struct {
	out     io.Writer
	width   int
	enabled bool
	web     bool
	visible bool
	ready   bool

	directory  string
	pdfPath    string
	html       []byte
	mediaPath  string
	deadline   time.Time
	generation uint64
	published  uint64
	displayed  uint64
	pending    *richRenderRequest
	targetID   string
	nextRender time.Time
	busy       bool
	endPending bool
	results    chan richRenderResult
}

func newRichPreview(out io.Writer, width int, enabled, web bool) (*richPreview, error) {
	view := &richPreview{out: out, width: width, enabled: enabled, web: web, results: make(chan richRenderResult, 1)}
	if !enabled || web {
		return view, nil
	}
	directory, err := os.MkdirTemp("", "opencode-tui-preview-")
	if err != nil {
		return nil, err
	}
	view.directory = directory
	view.pdfPath = filepath.Join(directory, "response.pdf")
	return view, nil
}

func (view *richPreview) command(command string) error {
	if view == nil || !view.enabled {
		return nil
	}
	return writeFull(view.out, []byte("\x1bP"+command+"\x1b\\"))
}

func (view *richPreview) canStart(columns, rows int) bool {
	return view != nil && view.enabled && rows >= 5 && columns*(100-view.width)/100 >= 8
}

func (view *richPreview) start(now time.Time) error {
	if view == nil || !view.enabled || view.visible {
		return nil
	}
	if err := view.command(fmt.Sprintf("region split %d%%; region secondary scroll report; region query", view.width)); err != nil {
		return err
	}
	view.visible = true
	view.ready = false
	view.deadline = now.Add(2 * time.Second)
	return nil
}

func (view *richPreview) toggle(now time.Time) error {
	if view == nil || !view.enabled {
		return nil
	}
	if !view.visible {
		return view.start(now)
	}
	if err := view.command("region reset"); err != nil {
		return err
	}
	view.visible, view.ready, view.displayed = false, false, 0
	return nil
}

func (view *richPreview) setReady(layout *regionLayout) error {
	if view == nil || !view.enabled || !view.visible || layout == nil {
		return nil
	}
	view.ready = true
	if view.published != 0 && view.displayed != view.published {
		return view.openPublished()
	}
	return nil
}

func (view *richPreview) request(messageID, text string) {
	if view == nil || !view.enabled || strings.TrimSpace(text) == "" {
		return
	}
	view.generation++
	view.targetID = messageID
	request := richRenderRequest{generation: view.generation, messageID: messageID, text: text}
	view.pending = &request
}

func (view *richPreview) requestMedia(key string, media *fileMedia) error {
	if view == nil || !view.enabled {
		return nil
	}
	if view.directory == "" {
		directory, err := os.MkdirTemp("", "opencode-tui-preview-")
		if err != nil {
			return err
		}
		view.directory, view.pdfPath = directory, filepath.Join(directory, "response.pdf")
	}
	view.generation++
	view.targetID = key
	view.pending = &richRenderRequest{generation: view.generation, messageID: key, media: media}
	return nil
}

func (view *richPreview) clear() error {
	if view == nil {
		return nil
	}
	view.generation++ // Invalidate a render still running for the previous session.
	view.pending, view.published, view.displayed = nil, 0, 0
	view.targetID = ""
	view.html = nil
	view.mediaPath = ""
	view.nextRender = time.Time{}
	view.endPending = false
	if view.visible {
		return view.command("region secondary clear")
	}
	return nil
}

func (view *richPreview) tick(now time.Time) (string, error) {
	if view == nil || !view.enabled {
		return "", nil
	}
	select {
	case result := <-view.results:
		view.busy = false
		if result.request.messageID == view.targetID {
			if result.request.generation == view.generation {
				view.pending = nil
			}
			if result.err != nil {
				return "Rich preview: " + result.err.Error(), nil
			}
			view.mediaPath = result.mediaPath
			if result.mediaPath != "" {
				view.html = nil
			} else if view.web {
				view.html = result.html
			} else if err := view.publish(result.pdf); err != nil {
				return "", err
			}
			view.published = result.request.generation
			if view.ready {
				if err := view.openPublished(); err != nil {
					return "", err
				}
			}
			if result.mathErrors > 0 {
				return fmt.Sprintf("Rich preview: %d mathematical expressions could not be rendered", result.mathErrors), nil
			}
		}
	default:
	}
	if view.visible && !view.ready && !view.deadline.IsZero() && !now.Before(view.deadline) {
		view.visible = false
		view.deadline = time.Time{}
		if err := view.command("region reset"); err != nil {
			return "", err
		}
		return "Rich preview unavailable (run inside st-go); using text response pane", nil
	}
	if !view.busy && view.pending != nil && !now.Before(view.nextRender) {
		request := *view.pending
		view.busy = true
		view.nextRender = now.Add(250 * time.Millisecond)
		results, web, directory := view.results, view.web, view.directory
		go func() {
			result := richRenderResult{request: request}
			if request.media != nil {
				var file *os.File
				file, result.err = os.CreateTemp(directory, "file-*"+request.media.extension)
				if result.err == nil {
					result.mediaPath = file.Name()
					_, result.err = file.Write(request.media.data)
					if err := file.Close(); result.err == nil {
						result.err = err
					}
				}
			} else if web {
				result.html, result.mathErrors, result.err = renderMarkdownHTML(request.text)
			} else {
				result.pdf, result.mathErrors, result.err = renderMarkdown(request.text, directory)
			}
			results <- result
		}()
	}
	return "", nil
}

func renderMarkdown(text, directory string) ([]byte, int, error) {
	path := filepath.Join(directory, "response.md")
	snapshot, err := preview.Load(path, func(string) (string, error) { return text, nil })
	if err != nil {
		return nil, 0, err
	}
	return preview.Render(snapshot)
}

func renderMarkdownHTML(text string) ([]byte, int, error) {
	snapshot, err := preview.Load("response.md", func(string) (string, error) { return text, nil })
	if err != nil {
		return nil, 0, err
	}
	body, mathErrors, err := preview.RenderHTML(snapshot)
	if err != nil {
		return nil, mathErrors, err
	}
	data, err := preview.EncodeHTML(api.HTMLDocument{HTML: body, CSS: preview.HTMLStyle + "body{background:#f7f5ef}"})
	return data, mathErrors, err
}

func (view *richPreview) publish(pdf []byte) error {
	next := view.pdfPath + ".next"
	defer os.Remove(next)
	if err := os.WriteFile(next, pdf, 0600); err != nil {
		return err
	}
	return os.Rename(next, view.pdfPath)
}

func quoteDSL(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (view *richPreview) openPublished() error {
	if view.published == 0 {
		return nil
	}
	var err error
	if view.mediaPath != "" {
		options := " fit-contain"
		if strings.HasSuffix(view.mediaPath, ".pdf") {
			options = " fit-width continuous"
		}
		err = view.command("region secondary open " + quoteDSL(view.mediaPath) + options)
	} else if view.web {
		err = preview.SendHTML(view.html, view.command)
	} else {
		err = view.command("region secondary open " + quoteDSL(view.pdfPath) + " background '#f7f5ef' fit-width continuous")
	}
	if err != nil {
		return err
	}
	view.displayed = view.published
	if view.endPending {
		return view.jumpToEnd()
	}
	return nil
}

func (view *richPreview) jumpToEnd() error {
	// A second click can arrive while the first render or region handshake is
	// still pending. Apply the jump after the document has actually been opened.
	view.endPending = true
	if !view.ready || view.displayed == 0 {
		return nil
	}
	if err := view.command("region secondary position 1000000"); err != nil {
		return err
	}
	view.endPending = false
	return nil
}

func (view *richPreview) close() error {
	if view == nil {
		return nil
	}
	var err error
	if view.visible {
		err = view.command("region reset")
	}
	view.visible, view.ready = false, false
	view.html = nil
	if view.directory != "" {
		if removeErr := os.RemoveAll(view.directory); err == nil {
			err = removeErr
		}
	}
	return err
}
