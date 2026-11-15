package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"st-go/aiharness"
	"st-go/tui/preview"
)

type inlineAIOptions struct {
	server, authorization, instruction string
	model                              *aiharness.Model
	envServer, envModel                string
}

type inlineAI struct {
	projects map[string]*aiharness.Harness
	last     map[*Buffer]string
}

func inlinePreviewText(path, text string) string {
	if preview.Supported(path) {
		return aiharness.ProjectText(text)
	}
	return text
}

func (e *Editor) inlineProject(b *Buffer) (*aiharness.Harness, aiharness.Project, error) {
	project, err := aiharness.Resolve(b.meta.Path, previewText(b))
	if err != nil {
		return nil, project, err
	}
	if e.inlineAI == nil {
		e.inlineAI = &inlineAI{projects: make(map[string]*aiharness.Harness), last: make(map[*Buffer]string)}
	}
	if h := e.inlineAI.projects[project.Database]; h != nil {
		return h, project, nil
	}
	h, err := aiharness.Open(project.Database, nil)
	if err != nil {
		return nil, project, err
	}
	e.inlineAI.projects[project.Database] = h
	return h, project, nil
}

// Like the TeX IMAP adapter, this runs only for an actual printable insert key;
// paste, registers and Ctrl-V quoted input remain literal. Generation is :AI.
func (e *Editor) expandInlineIMAP(key string) bool {
	b, pos := e.buffer(), e.pos()
	if key != "{" || pos < 2 || !preview.Supported(b.meta.Path) || string(b.text[pos-2:pos]) != "{{" {
		return false
	}
	if pos > 2 && (b.text[pos-3] == '\\' || b.text[pos-3] == '{') {
		return false
	}
	for _, block := range aiharness.Blocks(string(b.text)) {
		if pos >= block.Start && pos < block.End {
			return false
		}
	}
	id := aiharness.UUID()
	text := aiharness.BlockText(id, aiharness.Slot)
	at := pos - 2
	// Leave the cursor after the closed block, ready to continue writing.
	if !e.edit([]patch{{At: at, Old: "{{", New: text}}, at+utf8.RuneCountInString(text), e.insertGroup) {
		return true
	}
	e.insertText, e.insertCount = "", 1
	e.message = "AI slot " + id[:8] + " Initial · :AI requests unfilled slots"
	return true
}

func (e *Editor) inlineBackend(project aiharness.Project) (aiharness.Generator, *aiharness.Model, error) {
	server := e.inlineOptions.server
	if server == "" {
		server = project.Server
	}
	if server == "" {
		server = e.inlineOptions.envServer
	}
	model := e.inlineOptions.model
	if model == nil {
		value := project.Model
		if value == "" {
			value = e.inlineOptions.envModel
		}
		var err error
		model, err = aiharness.ParseModel(value)
		if err != nil {
			return nil, nil, err
		}
		if project.Variant != "" {
			if model == nil {
				return nil, nil, fmt.Errorf("%%variant requires %%model or GOVIM_AI_MODEL")
			}
			model.Variant = project.Variant
		}
	}
	client, err := aiharness.NewOpenCode(server, e.inlineOptions.authorization)
	return client, model, err
}

func (e *Editor) inlineRequest(id string) error {
	defer e.inlineStatusChanged()
	b := e.buffer()
	text := previewText(b)
	blocks := aiharness.Blocks(text)
	if len(blocks) == 0 {
		return fmt.Errorf("type {{{ in Insert mode to create AI slots first")
	}
	var selected []aiharness.Block
	for _, block := range blocks {
		if id != "" && block.UUID != id {
			continue
		}
		if _, err := aiharness.FindBlock(text, block.UUID); err != nil {
			return err
		}
		if string([]rune(text)[block.Content:block.Close]) == aiharness.Slot {
			selected = append(selected, block)
		}
	}
	if id != "" && len(selected) == 0 {
		return fmt.Errorf("no empty AI slot for UUID %s", id)
	}
	h, project, err := e.inlineProject(b)
	if err != nil {
		return err
	}
	context, err := aiharness.ReadContext(project.ContextFile)
	if err != nil {
		return err
	}
	var pending []string
	for _, block := range selected {
		r, err := h.Latest(block.UUID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if r.Document != b.meta.Path {
				return fmt.Errorf("AI UUID belongs to another document")
			}
			switch r.Status {
			case "ready", "inserted":
				continue
			case "queued", "running":
				if !h.Active(r) {
					return fmt.Errorf("AI %s unavailable in this editor; :UnIns before retrying", block.UUID[:8])
				}
				continue
			}
		}
		pending = append(pending, block.UUID)
	}
	if len(pending) == 0 {
		e.message = "No Initial AI slots · Ctrl-J inserts ready paragraphs"
		return nil
	}
	client, model, err := e.inlineBackend(project)
	if err != nil {
		return err
	}
	if _, err := h.SubmitBatchWith(aiharness.BatchRequest{UUIDs: pending, Document: b.meta.Path, Instruction: e.inlineOptions.instruction, Context: context, CurrentDocument: text, Model: model}, client); err != nil {
		return err
	}
	e.inlineAI.last[b] = pending[len(pending)-1]
	e.message = fmt.Sprintf("AI 1 request · %d slots Processing · Ctrl-J inserts when ready", len(pending))
	e.dirty = true
	return nil
}

// jumpInlinePlaceholder intercepts only a real AI slot; ordinary IMAP stops
// continue through the existing placeholder implementation.
func (e *Editor) jumpInlinePlaceholder(target, last int) bool {
	b := e.buffer()
	text := string(b.text)
	for _, block := range aiharness.Blocks(text) {
		if target < block.Content || target >= block.Close {
			continue
		}
		if _, err := aiharness.FindBlock(text, block.UUID); err != nil {
			e.error(err)
			return true
		}
		if target != block.Content || last != block.Close {
			e.error(fmt.Errorf("AI slot was edited; :UnIns restores it"))
			return true
		}
		e.setPos(target)
		h, _, err := e.inlineProject(b)
		if err != nil {
			e.error(err)
			return true
		}
		e.inlineAI.last[b] = block.UUID
		r, err := h.Latest(block.UUID)
		if errors.Is(err, sql.ErrNoRows) {
			e.message = "AI slot Initial · :AI requests unfilled slots"
			return true
		}
		if err != nil {
			e.error(err)
			return true
		}
		if r.Document != b.meta.Path {
			e.error(fmt.Errorf("AI UUID belongs to another document"))
			return true
		}
		switch r.Status {
		case "discarded":
			e.message = "AI slot Initial · :AI requests a new version"
		case "queued", "running":
			e.message = "AI paragraph is still generating · Ctrl-J again when ready"
			if !h.Active(r) {
				e.message = "AI request is unavailable in this editor · :UnIns then :AI retries explicitly"
			}
		case "ready", "inserted":
			oldMode := e.mode
			if oldMode != insertMode {
				e.enterInsert(insertMode, 0, 1)
			}
			// Acceptance is its own undo operation, independent of earlier typing.
			group := b.group()
			if !e.edit([]patch{{At: target, Old: aiharness.Slot, New: r.Paragraph}}, target+utf8.RuneCountInString(r.Paragraph), group) {
				e.mode = oldMode
				return true
			}
			e.insertGroup = b.group()
			e.insertText, e.insertCount, e.replaceBack = "", 1, nil
			if err := h.RecordAction(r, "inserted", r.Paragraph); err != nil {
				e.error(err)
				return true
			}
			e.inlineRefreshPreview()
		case "failed", "unknown":
			e.error(fmt.Errorf("AI %s: %s · :AI retries explicitly", r.Status, r.Error))
		default:
			e.error(fmt.Errorf("unknown AI state %q", r.Status))
		}
		return true
	}
	return false
}

func (e *Editor) inlineUninsert(id string) error {
	defer e.inlineStatusChanged()
	b := e.buffer()
	text := string(b.text)
	if id == "" {
		for _, block := range aiharness.Blocks(text) {
			if e.pos() >= block.Start && e.pos() <= block.End {
				id = block.UUID
				break
			}
		}
		if id == "" && e.inlineAI != nil {
			id = e.inlineAI.last[b]
		}
	}
	if id == "" {
		return fmt.Errorf("place the cursor in an AI block, then :UnIns")
	}
	block, err := aiharness.FindBlock(text, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	h, _, err := e.inlineProject(b)
	if err != nil {
		return err
	}
	r, err := h.Latest(block.UUID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && r.Document != b.meta.Path {
		return fmt.Errorf("AI UUID belongs to another document")
	}
	body := string(b.text[block.Content:block.Close])
	if body != aiharness.Slot {
		if !e.edit([]patch{{At: block.Content, Old: body, New: aiharness.Slot}}, block.Content, 0) {
			return fmt.Errorf("cannot discard AI insertion")
		}
	}
	if r.Version != 0 {
		if err := h.RecordAction(r, "discarded", body); err != nil {
			return err
		}
	}
	e.inlineAI.last[b] = block.UUID
	e.setPos(block.Content)
	e.inlineRefreshPreview()
	e.message = "AI insertion discarded · :AI requests a new version · u undoes the source edit"
	return nil
}

func (e *Editor) inlineRefreshPreview() {
	if e.preview != nil {
		e.error(e.preview.toggle(e, true))
	} else {
		e.message = "AI paragraph inserted (preview disabled)"
	}
}

func (e *Editor) inlineTick() {
	if e.inlineAI == nil {
		return
	}
	for _, h := range e.inlineAI.projects {
		for draining := true; draining; {
			select {
			case r := <-h.Updates():
				e.inlineStatusChanged()
				// A result can be queued just before UnIns or acceptance. Only
				// advertise the dictionary version/state that is still current.
				latest, err := h.Latest(r.UUID)
				if err == nil && (latest.Version != r.Version || latest.Status != r.Status) {
					continue
				}
				e.message = fmt.Sprintf("AI %s v%d %s", r.UUID[:8], r.Version, r.Status)
				if r.Status == "ready" {
					e.message += " · Ctrl-J to insert"
				}
				if r.Error != "" {
					e.message += " · " + r.Error
				}
				e.dirty = true
			default:
				draining = false
			}
		}
	}
}

func (e *Editor) inlineStatusChanged() {
	if e.preview != nil {
		e.preview.slotCheck = time.Time{}
	}
}

func (e *Editor) closeInlineAI() error {
	if e.inlineAI == nil {
		return nil
	}
	var first error
	for _, h := range e.inlineAI.projects {
		if err := h.Close(); first == nil {
			first = err
		}
	}
	e.inlineAI = nil
	return first
}
