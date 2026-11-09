package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"

	"st-go/limits"
	"st-go/term"
	"st-go/tui/outline"
	"st-go/tui/preview"
)

type editorTOC struct {
	active, sent bool
	buffer       *Buffer
	version      uint64
	token        int
	entries      []outline.Entry
	items        []term.RegionListItem
}

func (p *editorPreview) toggleContents(e *Editor) error {
	if p == nil || p.closed {
		return fmt.Errorf("contents panel is disabled (omit --no-preview)")
	}
	if p.unsupported {
		return fmt.Errorf("contents panel requires updated st-go display regions")
	}
	p.observe(e)
	if p.path == "" {
		return fmt.Errorf("contents panel requires a named LaTeX/Markdown buffer")
	}
	if p.toc == nil {
		p.toc = &editorTOC{}
	}
	if p.toc.active {
		return p.restorePreview()
	}
	p.toc.active, p.toc.sent = true, false
	p.commands = nil
	return nil
}

func (p *editorPreview) restorePreview() error {
	if p.toc == nil || !p.toc.active {
		return nil
	}
	if p.toc.sent && p.split && p.ready {
		if err := p.command("region secondary list restore"); err != nil {
			return err
		}
	}
	p.toc.active, p.toc.sent = false, false
	p.commands = nil
	return nil
}

func (p *editorPreview) drawContents(e *Editor) error {
	toc, b := p.toc, e.buffer()
	if toc.sent && toc.buffer == b && toc.version == b.version {
		return nil
	}
	entries := outline.TeX(b.text)
	if preview.Markdown(b.meta.Path) {
		entries = outline.Markdown(b.text)
	}
	items := make([]term.RegionListItem, 0, len(entries))
	bytes := 0
	for i, entry := range entries {
		bytes += len(entry.Title)
		if bytes > limits.ControlBytes/2-256 {
			entries = entries[:i]
			break
		}
		items = append(items, term.RegionListItem{ID: i + 1, Level: entry.Level, Text: entry.Title})
	}
	same := toc.sent && toc.buffer == b && slices.Equal(toc.items, items)
	// Locations track unsaved edits even when no visible list item changed.
	// Do not resend or repaint an identical TOC merely because offsets shifted.
	toc.buffer, toc.version, toc.entries = b, b.version, entries
	if same {
		return nil
	}
	token := toc.token%(1<<30) + 1
	list := term.RegionList{Token: token, Title: "Table of contents", Items: items}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	if len(encoded) > limits.ControlBytes-128 {
		return fmt.Errorf("contents panel exceeds protocol limit")
	}
	if err := p.command("region secondary list " + encoded); err != nil {
		return err
	}
	toc.items, toc.token, toc.sent = items, token, true
	return nil
}

func (p *editorPreview) activateContents(e *Editor, event previewEvent) {
	if p.toc == nil || !p.toc.active || !p.ready || !p.toc.sent {
		return
	}
	// Refresh source positions first. Structural changes give the displayed
	// list a new token, so a late click cannot jump to an unrelated entry.
	if err := p.drawContents(e); err != nil {
		e.error(err)
		return
	}
	toc := p.toc
	if event.token != toc.token || event.value < 1 || event.value > len(toc.entries) || e.prompt != "" {
		return
	}
	e.addJump()
	e.setPos(toc.entries[event.value-1].Offset)
	w := e.window()
	w.topLine, w.topWrap = max(0, w.buffer.row(w.cursor)-2), 0
	e.dirty = true
}
