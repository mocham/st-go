package main

import (
	"path/filepath"
	"sort"

	"st-go/tui/preview"
)

func (e *Editor) aiPreview(selected *aiProposal, jump bool) {
	a, p := e.ai, e.preview
	if a == nil || p == nil || p.closed || p.unsupported {
		return
	}
	if jump {
		a.focus = selected.ID
		a.viewDocument = filepath.Join(a.store.root, selected.Document)
	}
	if a.viewDocument == "" {
		a.viewDocument = filepath.Join(a.store.root, selected.Document)
	}
	p.observe(e)
	if p.toc != nil {
		p.toc.active = false
	}
	p.serial++
	request := &previewRequest{web: p.options.web, id: p.serial, path: a.viewDocument, noMove: !jump, immediate: true, navigation: p.navigation}
	e.aiAnnotate(request)
	if jump {
		request.focusAnchor = "ai-" + selected.ID
	}
	p.pending = request
	p.completed = nil
}

func (e *Editor) aiAnnotate(request *previewRequest) {
	a := e.ai
	if a == nil || a.viewDocument == "" {
		return
	}
	request.forceRender = true
	request.overrides = make(map[string]string)
	for _, b := range e.buffers {
		if b.meta.Path != "" {
			request.overrides[b.meta.Path] = previewText(b)
		}
	}
	for _, proposal := range a.proposals {
		if proposal.Status == "inserted" || proposal.Status == "dismissed" {
			continue
		}
		draft := proposal.Draft
		if proposal.Status != "result" {
			draft = ""
		}
		request.annotations = append(request.annotations, preview.Annotation{ID: proposal.ID, Document: proposal.Document, Text: draft, Reasoning: proposal.Reasoning, Status: proposal.Status, Error: proposal.Error, ShowReasoning: a.showReasoning || proposal.Status != "result"})
	}
	sort.Slice(request.annotations, func(i, j int) bool { return request.annotations[i].ID < request.annotations[j].ID })
}
