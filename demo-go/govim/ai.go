package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"st-go/tui/appbus"
	"st-go/tui/preview"
	"st-go/tui/writing"
)

type editorAI struct {
	store                *aiStore
	bus                  *appbus.Client
	provider             string
	providers            map[string]time.Time
	proposals            map[string]*aiProposal
	focus                string
	draft                *Buffer
	source               *Buffer
	draftProposal        string
	draftSaved           string
	viewDocument         string
	showReasoning        bool
	outbox               []writing.Message
	lastHello, lastDraft time.Time
	lastStatus           time.Time
	revisions            map[*Buffer]uint64
	picker               []*aiProposal
	pickerIndex          int
	pickerCommand        string
	pickerArgs           string
}

func (e *Editor) ensureAI() error {
	if e.ai != nil {
		return nil
	}
	root := e.aiProject
	if root == "" {
		root = writing.Root(e.buffer().meta.Path)
	} else {
		var err error
		root, err = canonicalPath(root)
		if err != nil {
			return err
		}
	}
	channel := e.aiChannel
	if channel == "" {
		channel = writing.Channel(root)
	}
	bus, err := appbus.New(os.Stdout, os.Getenv("ST_GO_APPBUS_TOKEN"), channel)
	if err != nil {
		return err
	}
	store, err := openAIStore(root)
	if err != nil {
		return err
	}
	all, err := store.all()
	if err != nil {
		store.db.Close()
		return err
	}
	a := &editorAI{store: store, bus: bus, providers: make(map[string]time.Time), proposals: make(map[string]*aiProposal), revisions: make(map[*Buffer]uint64)}
	for _, p := range all {
		a.proposals[p.ID] = p
	}
	e.ai = a
	a.outbox = append(a.outbox, writing.Message{Type: "hello", Project: root})
	return nil
}

func (e *Editor) aiCommand(name, args string) {
	if err := e.ensureAI(); err != nil {
		e.error(err)
		return
	}
	a := e.ai
	switch name {
	case "AIBack":
		if err := e.aiSaveDraft(); err != nil {
			e.error(err)
			return
		}
		if e.buffer() == a.draft && a.source != nil {
			e.switchBuffer(a.source)
		}
		a.viewDocument = ""
		if e.preview != nil {
			e.preview.observe(e)
			e.error(e.preview.toggle(e, true))
		}
	case "AIConnect":
		if args != "" {
			if _, ok := a.providers[args]; !ok {
				e.error(fmt.Errorf("unknown provider; run :AIConnect to list"))
				return
			}
			a.provider = args
		}
		e.message = "AI channel " + a.bus.Channel
		if a.provider != "" {
			e.message += " connected to " + a.provider
		} else {
			e.message += "; waiting for opencode-tui"
		}
		if len(a.providers) > 1 {
			e.pager = []string{"Choose a provider with :AIConnect ID"}
			for id := range a.providers {
				e.pager = append(e.pager, id)
			}
		}
	case "AINext":
		e.error(e.aiNext(args))
	case "AIList":
		e.aiList()
	case "AIView", "AIEdit", "AIIns", "AIDismiss", "AIRefine", "AIRetry", "AIReasoning", "AICancel":
		selection := args
		if name == "AIRefine" {
			selection = ""
		}
		p := e.aiSelect(selection)
		if p == nil {
			if selection != "" {
				e.error(fmt.Errorf("unknown or ambiguous proposal ID"))
				return
			}
			e.aiPick(name)
			a.pickerArgs = args
			return
		}
		a.focus = p.ID
		switch name {
		case "AIView":
			e.aiPreview(p, true)
		case "AIReasoning":
			a.showReasoning = !a.showReasoning
			e.aiPreview(p, true)
		case "AIEdit":
			e.error(e.aiEdit(p))
		case "AIIns":
			e.error(e.aiInsert(p))
		case "AIDismiss":
			a.outbox = append(a.outbox, writing.Message{Type: "cancel", To: a.provider, RequestID: p.Request})
			e.error(a.store.state(p, "dismissed"))
			e.error(a.store.event(p, "dismissed", ""))
			e.aiPreview(p, false)
		case "AICancel":
			a.outbox = append(a.outbox, writing.Message{Type: "cancel", To: a.provider, RequestID: p.Request})
		case "AIRefine", "AIRetry":
			if err := e.aiSaveDraft(); err != nil {
				e.error(err)
				return
			}
			instruction := p.Instruction
			if name == "AIRefine" {
				instruction = args
			}
			e.error(e.aiSubmit(p, instruction))
			e.aiPreview(p, true)
		}
	}
}

func (e *Editor) aiNext(instruction string) error {
	b := e.buffer()
	a := e.ai
	if a.draft == b {
		return fmt.Errorf("use :AIRefine while editing a proposal")
	}
	if b.meta.Path == "" || !preview.Supported(b.meta.Path) {
		return fmt.Errorf("AI paragraphs require a named Markdown or LaTeX buffer")
	}
	if !writing.Within(a.store.root, b.meta.Path) {
		return fmt.Errorf("document belongs to another AI project")
	}
	id := writing.UUID()
	marker := writing.Marker(b.meta.Path, id)
	row := b.row(e.pos())
	for row < len(b.lineStarts())-1 && strings.TrimSpace(string(b.line(row))) != "" {
		row++
	}
	at := b.start(row)
	if row == len(b.lineStarts())-1 && strings.TrimSpace(string(b.line(row))) != "" {
		at = len(b.text)
	}
	prefix := ""
	if at > 0 && b.text[at-1] != '\n' {
		prefix = "\n\n"
	} else if at > 1 && b.text[at-2] != '\n' {
		prefix = "\n"
	}
	if !e.edit([]patch{{At: at, New: prefix + marker + "\n\n"}}, at+len([]rune(prefix)), 0) {
		return fmt.Errorf("cannot create AI placeholder")
	}
	document, _ := filepath.Rel(a.store.root, b.meta.Path)
	p := &aiProposal{ID: id, Document: document}
	a.proposals[id] = p
	a.focus = id
	if err := a.store.placeholder(p); err != nil {
		return err
	}
	if err := e.aiSubmit(p, instruction); err != nil {
		p.Status = "failed"
		p.Error = err.Error()
		return err
	}
	e.aiPreview(p, true)
	return nil
}

func (e *Editor) aiBuffer(p *aiProposal) (*Buffer, error) {
	path := filepath.Join(e.ai.store.root, p.Document)
	for _, b := range e.buffers {
		if b.meta.Path == path {
			return b, nil
		}
	}
	return e.open(path, false)
}

func (e *Editor) aiSubmit(p *aiProposal, instruction string) error {
	a := e.ai
	if len(instruction) > 16384 {
		return fmt.Errorf("AI instruction too long")
	}
	b, err := e.aiBuffer(p)
	if err != nil {
		return err
	}
	marker := writing.Marker(b.meta.Path, p.ID)
	if strings.Count(string(b.text), marker) != 1 {
		return fmt.Errorf("placeholder missing or duplicated")
	}
	id := appbus.ID()
	dir := filepath.Join(a.store.root, ".govim", "ai-snapshots")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	texts := make(map[string]string)
	revisions := make(map[string]uint64)
	for _, buffer := range e.buffers {
		if buffer.meta.Path != "" && writing.Within(a.store.root, buffer.meta.Path) && !writing.Within(filepath.Join(a.store.root, ".govim"), buffer.meta.Path) {
			texts[buffer.meta.Path] = previewText(buffer)
			revisions[buffer.meta.Path] = buffer.syncRevision
		}
	}
	reader := func(path string) (string, error) {
		if text, ok := texts[path]; ok {
			return text, nil
		}
		buffer, err := loadBuffer(path)
		if err != nil {
			return "", err
		}
		if !buffer.meta.Existed {
			return "", os.ErrNotExist
		}
		text := previewText(buffer)
		if writing.Within(a.store.root, path) {
			texts[path] = text
		}
		return text, nil
	}
	// Expand includes once to freeze the root and all reachable source fragments.
	if _, err := preview.Load(b.meta.Path, reader); err != nil {
		return err
	}
	manifest := writing.Manifest{Version: writing.Version, Project: a.store.root, Document: p.Document, Placeholder: p.ID, CursorRune: e.pos()}
	if b != e.buffer() {
		manifest.CursorRune = b.recoveryCursor
	}
	paths := make([]string, 0, len(texts))
	for path := range texts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		text := texts[path]
		hash := writing.Hash(text)
		object := filepath.Join(dir, hash+".txt")
		if _, err := os.Stat(object); os.IsNotExist(err) {
			if err := atomicAIFile(object, []byte(text)); err != nil {
				return err
			}
		}
		rel, _ := filepath.Rel(a.store.root, path)
		manifest.Files = append(manifest.Files, writing.File{Path: rel, Snapshot: object, Hash: hash, Revision: revisions[path]})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, id+".json")
	if err := atomicAIFile(path, data); err != nil {
		return err
	}
	next := *p
	next.Request, next.Manifest, next.Instruction, next.Status, next.Error = id, path, instruction, "queued", ""
	if err := a.store.request(&next); err != nil {
		return err
	}
	if p.Request != "" && p.Status != "result" && p.Status != "failed" && p.Status != "cancelled" {
		a.outbox = append(a.outbox, writing.Message{Type: "cancel", RequestID: p.Request})
	}
	*p = next
	session := p.Session
	if session == "" {
		for _, previous := range a.proposals {
			if previous.Document == p.Document && previous.Session != "" {
				session = previous.Session
				break
			}
		}
	}
	a.outbox = append(a.outbox, writing.Message{Type: "suggest", RequestID: id, Placeholder: p.ID, Project: a.store.root, Document: p.Document, Manifest: path, Instruction: instruction, Draft: p.Draft, Session: session})
	e.message = "AI paragraph queued · " + p.ID + " (continue editing; :AIIns to insert)"
	return nil
}
func atomicAIFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".ai-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (e *Editor) aiSelect(id string) *aiProposal {
	a := e.ai
	if id != "" {
		if p := a.proposals[id]; p != nil {
			return p
		}
		var found *aiProposal
		for key, p := range a.proposals {
			if strings.HasPrefix(key, id) {
				if found != nil {
					return nil
				}
				found = p
			}
		}
		return found
	}
	line := string(e.buffer().line(e.buffer().row(e.pos())))
	for id, p := range a.proposals {
		if strings.Contains(line, "govim-ai:"+id) {
			return p
		}
	}
	if e.buffer() == a.draft {
		return a.proposals[a.draftProposal]
	}
	return nil
}
func (e *Editor) aiList() {
	e.pager = []string{"AI project: " + e.ai.store.root, "Use :AIView UUID, :AIEdit UUID, :AIIns UUID, :AIRetry UUID"}
	ids := make([]string, 0, len(e.ai.proposals))
	for id := range e.ai.proposals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := e.ai.proposals[id]
		e.pager = append(e.pager, id+"  "+p.Status+"  "+p.Document)
	}
}
func (e *Editor) aiEdit(p *aiProposal) error {
	if p.Status != "result" {
		return fmt.Errorf("prediction is not ready")
	}
	if err := e.aiSaveDraft(); err != nil {
		return err
	}
	source, err := e.aiBuffer(p)
	if err != nil {
		return err
	}
	dir := filepath.Join(e.ai.store.root, ".govim", "ai-drafts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, p.ID+"-"+p.Request+filepath.Ext(p.Document))
	var draft *Buffer
	for _, b := range e.buffers {
		if b.meta.Path == path {
			draft = b
		}
	}
	if draft == nil {
		if err := atomicAIFile(path, []byte(p.Draft)); err != nil {
			return err
		}
		draft, err = e.open(path, false)
		if err != nil {
			return err
		}
	}
	e.ai.source, e.ai.draft, e.ai.draftProposal, e.ai.draftSaved = source, draft, p.ID, string(draft.text)
	e.switchBuffer(draft)
	e.aiPreview(p, true)
	e.message = "Editing AI draft · :AIIns to insert; :AIRefine instruction to revise"
	return nil
}
func (e *Editor) aiSaveDraft() error {
	a := e.ai
	if a == nil || a.draft == nil {
		return nil
	}
	p := a.proposals[a.draftProposal]
	if p == nil {
		return nil
	}
	text := string(a.draft.text)
	if text == a.draftSaved {
		return nil
	}
	if len(text) > writing.MaxResponse {
		return fmt.Errorf("AI draft exceeds size limit")
	}
	if err := a.store.adapt(p, text, "edit"); err != nil {
		return err
	}
	if err := a.draft.save(false); err != nil {
		return err
	}
	p.Draft = text
	a.draftSaved = text
	e.aiPreview(p, false)
	return nil
}
func (e *Editor) aiInsert(p *aiProposal) error {
	if err := e.aiSaveDraft(); err != nil {
		return err
	}
	if p.Status != "result" || strings.TrimSpace(p.Draft) == "" {
		return fmt.Errorf("no complete prediction to insert")
	}
	b, err := e.aiBuffer(p)
	if err != nil {
		return err
	}
	marker := writing.Marker(b.meta.Path, p.ID)
	text := string(b.text)
	if strings.Count(text, marker) != 1 {
		return fmt.Errorf("placeholder missing or duplicated; insertion cancelled")
	}
	at := utf8.RuneCountInString(text[:strings.Index(text, marker)])
	acceptance, err := e.ai.store.insertIntent(p)
	if err != nil {
		return err
	}
	e.switchBuffer(b)
	if !e.edit([]patch{{At: at, Old: marker, New: p.Draft}}, at+utf8.RuneCountInString(p.Draft), 0) {
		return fmt.Errorf("insertion failed")
	}
	if err := e.ai.store.inserted(p, acceptance, writing.Hash(string(b.text))); err != nil {
		return err
	}
	if e.ai.draftProposal == p.ID {
		e.ai.draft = nil
		e.ai.draftProposal = ""
	}
	e.aiPreview(p, false)
	e.ai.viewDocument = ""
	e.message = "AI paragraph inserted · u to undo"
	return nil
}

func (e *Editor) aiReceive(control string) {
	a := e.ai
	if a == nil {
		return
	}
	frame, err := a.bus.Receive(control)
	if err != nil {
		e.error(err)
		return
	}
	if frame == nil || frame.Sender == a.bus.ID {
		return
	}
	var m writing.Message
	if json.Unmarshal(frame.Data, &m) != nil || m.Version != writing.Version || m.To != "" && m.To != a.bus.ID {
		return
	}
	if m.Type == "ready" && m.Project == a.store.root {
		a.providers[frame.Sender] = time.Now()
		if a.provider == "" && len(a.providers) == 1 {
			a.provider = frame.Sender
		}
		return
	}
	if frame.Sender != a.provider {
		return
	}
	var p *aiProposal
	for _, candidate := range a.proposals {
		if candidate.Request == m.RequestID {
			p = candidate
			break
		}
	}
	if p == nil {
		// A superseded generation is still historical evidence. It must never
		// replace the current adaptation, but its final response is retained.
		var exists int
		if a.store.db.QueryRow("SELECT count(*) FROM requests WHERE id=?", m.RequestID).Scan(&exists) == nil && exists == 1 {
			if m.Type == "result" || m.Type == "failed" || m.Type == "cancelled" {
				e.error(a.store.response(m))
			}
		}
		return
	}
	if m.Type == "unknown" {
		p.Status = "unknown"
		_ = a.store.state(p, "unknown")
		e.message = "AI request unavailable after provider restart; :AIRetry " + p.ID
		e.dirty = true
		return
	}
	if m.Type != "progress" && m.Type != "pending" && m.Type != "result" && m.Type != "failed" && m.Type != "cancelled" {
		return
	}
	if p.Status == "result" || p.Status == "inserted" || p.Status == "dismissed" || p.Status == "failed" || p.Status == "cancelled" {
		return
	}
	if len(m.Text)+len(m.Reasoning)+len(m.Prediction) > appbus.MaxMessage {
		return
	}
	if m.Type == "result" {
		prediction, err := writing.Prediction(m.Text, m.RequestID)
		if err != nil || prediction != m.Prediction {
			m.Type = "failed"
			m.Error = "invalid prediction block"
		}
	}
	if err := a.store.response(m); err != nil {
		e.error(err)
		return
	}
	p.Status, p.Text, p.Reasoning, p.Session, p.Error = m.Type, m.Text, m.Reasoning, m.Session, m.Error
	if m.Type == "result" {
		p.Draft = m.Prediction
		a.showReasoning = false
		a.focus = p.ID
		e.message = "AI paragraph ready · :AIEdit to adapt · :AIIns to insert"
		if a.draftProposal == p.ID {
			a.draft = nil
			a.draftProposal = ""
		}
	}
	if m.Error != "" {
		e.message = m.Error
	}
	e.dirty = true
	e.aiPreview(p, m.Type == "result")
}
func (e *Editor) aiTick(now time.Time) {
	a := e.ai
	if a == nil {
		return
	}
	if err := a.bus.Tick(now); err != nil {
		e.error(err)
	}
	if now.Sub(a.lastHello) > 2*time.Second {
		a.lastHello = now
		for id, t := range a.providers {
			if now.Sub(t) > 6*time.Second {
				delete(a.providers, id)
				if a.provider == id {
					a.provider = ""
				}
			}
		}
		a.outbox = append(a.outbox, writing.Message{Type: "hello", Project: a.store.root})
	}
	if a.provider != "" && now.Sub(a.lastStatus) > 4*time.Second {
		a.lastStatus = now
		for _, p := range a.proposals {
			if p.Status == "pending" || p.Status == "progress" || p.Status == "queued" || p.Status == "sent" {
				a.outbox = append(a.outbox, writing.Message{Type: "status", RequestID: p.Request, Session: p.Session, Project: a.store.root, Document: p.Document, Placeholder: p.ID, Manifest: p.Manifest})
			}
		}
	}
	var pending []writing.Message
	for _, m := range a.outbox {
		if m.Type == "suggest" {
			p := a.proposals[m.Placeholder]
			if p == nil || p.Request != m.RequestID || p.Status != "queued" {
				continue
			}
		}
		if m.Type != "hello" && a.provider == "" {
			pending = append(pending, m)
			continue
		}
		m.Version = writing.Version
		if m.Type != "hello" {
			m.To = a.provider
		}
		if err := a.bus.Publish(m); err != nil {
			e.error(err)
		} else if m.Type == "suggest" {
			if p := a.proposals[m.Placeholder]; p != nil && p.Request == m.RequestID {
				e.error(a.store.state(p, "sent"))
			}
		}
	}
	a.outbox = pending
	if now.Sub(a.lastDraft) > 400*time.Millisecond {
		a.lastDraft = now
		e.error(e.aiSaveDraft())
		changed := false
		for _, b := range e.buffers {
			if a.revisions[b] != b.syncRevision {
				changed = true
				a.revisions[b] = b.syncRevision
			}
		}
		if changed {
			for _, p := range a.proposals {
				if p.Status == "inserted" {
					for _, b := range e.buffers {
						if b.meta.Path == filepath.Join(a.store.root, p.Document) && strings.Count(string(b.text), writing.Marker(p.Document, p.ID)) == 1 {
							e.error(a.store.state(p, "result"))
							e.error(a.store.event(p, "insertion-undone", ""))
						}
					}
				}
			}
			if p := a.proposals[a.focus]; p != nil {
				e.aiPreview(p, false)
			}
		}
	}
}
