package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"st-go/tui/readline"
)

const filterPrefix = "Filter: "

type pickerOption struct {
	label, detail string
	action        string // session, new, more, or model
	sessionID     string
	model         modelRef
	current       bool
}

type picker struct {
	kind       string
	input      *readline.Line
	options    []pickerOption
	selected   int
	top        int
	next       string
	request    uint64
	cancel     context.CancelFunc
	loading    bool
	submitting bool
	searchAt   time.Time
	err        string
}

type pickerResult struct {
	request uint64
	options []pickerOption
	next    string
	append  bool
	model   modelRef
}

func (application *app) slashCommand(text string) bool {
	command, query, _ := strings.Cut(text, " ")
	switch command {
	case "/sessions", "/models", "/variants":
		application.openPicker(strings.TrimPrefix(command, "/"), strings.TrimSpace(query))
		return true
	}
	return false
}

func (application *app) closePicker() {
	if application.picker == nil {
		return
	}
	if application.picker.cancel != nil {
		application.picker.cancel()
	}
	application.picker.input.Close()
	application.picker = nil
	application.dirty = true
}

func (application *app) openPicker(kind, query string) {
	application.closePicker()
	application.picker = &picker{kind: kind, input: readline.New(filterPrefix, len([]rune(filterPrefix)), 1024)}
	_ = application.picker.input.Insert(query)
	application.loadPicker(false)
}

func (picker *picker) query() string {
	return strings.TrimSpace(string([]rune(picker.input.Text())[picker.input.Min:]))
}

func (picker *picker) visible() []pickerOption {
	if picker.kind == "sessions" {
		return picker.options // Search across all pages is performed by the server.
	}
	var options []pickerOption
	words := strings.Fields(strings.ToLower(picker.query()))
	for _, option := range picker.options {
		text := strings.ToLower(option.label + " " + option.detail)
		match := true
		for _, word := range words {
			match = match && strings.Contains(text, word)
		}
		if match {
			options = append(options, option)
		}
	}
	return options
}

// Each request owns a token and a cancellation context. Neither a canceled
// search nor a response for a previously attached session may change the UI.
func (application *app) pickerRequest(kind string, call func(context.Context) (operationResult, error)) {
	picker := application.picker
	if picker.cancel != nil {
		picker.cancel()
	}
	application.pickerSequence++
	picker.request = application.pickerSequence
	request := picker.request
	id, epoch := application.session.ID, application.sessionEpoch
	activityRevision := application.activityRevision
	ctx, cancel := context.WithTimeout(application.ctx, 30*time.Second)
	picker.cancel = cancel
	picker.loading, picker.err = true, ""
	application.dirty = true
	go func() {
		defer cancel()
		result, err := call(ctx)
		if result.picker == nil {
			result.picker = &pickerResult{}
		}
		result.kind, result.sessionID, result.epoch, result.err = kind, id, epoch, err
		result.activityRevision = activityRevision
		result.picker.request = request
		select {
		case application.operations <- result:
		case <-ctx.Done():
			// Deliver timeout errors too, unless the application has exited.
			select {
			case application.operations <- result:
			case <-application.ctx.Done():
			}
		}
	}()
}

func (application *app) loadPicker(more bool) {
	picker := application.picker
	kind, query, cursor := picker.kind, picker.query(), ""
	if more {
		cursor = picker.next
	}
	session := application.session
	application.pickerRequest("picker-list", func(ctx context.Context) (operationResult, error) {
		result := operationResult{picker: &pickerResult{append: more}}
		if kind == "sessions" {
			page, err := application.api.sessions(ctx, query, cursor)
			if err != nil {
				return result, err
			}
			for _, item := range page.Data {
				label := item.Title
				if strings.TrimSpace(label) == "" {
					label = item.ID
				}
				result.picker.options = append(result.picker.options, pickerOption{
					label: label, detail: item.ID + " · " + item.Location.Directory,
					action: "session", sessionID: item.ID, current: item.ID == session.ID,
				})
			}
			result.picker.next = page.Cursor.Next
			return result, nil
		}
		models, err := application.api.models(ctx, session.Location.Directory)
		if err != nil {
			return result, err
		}
		if kind == "models" {
			for _, model := range models {
				if !model.Enabled {
					continue
				}
				ref := modelRef{ID: model.ID, ProviderID: model.ProviderID}
				current := session.Model != nil && session.Model.ID == ref.ID && session.Model.ProviderID == ref.ProviderID
				// Reselecting the current model preserves its chosen variant.
				if current {
					ref.Variant = session.Model.Variant
				}
				result.picker.options = append(result.picker.options, pickerOption{
					label: model.Name, detail: model.ProviderID + "/" + model.ID, action: "model", model: ref, current: current,
				})
			}
			return result, nil
		}
		var model *modelInfo
		if session.Model == nil {
			model, err = application.api.defaultModel(ctx, session.Location.Directory)
			if err != nil {
				return result, err
			}
		} else {
			for index := range models {
				if models[index].ID == session.Model.ID && models[index].ProviderID == session.Model.ProviderID {
					model = &models[index]
					break
				}
			}
		}
		if model == nil {
			return result, fmt.Errorf("current model is unavailable; select one with /models")
		}
		ref := modelRef{ID: model.ID, ProviderID: model.ProviderID}
		variant := ""
		if session.Model != nil {
			variant = session.Model.Variant
		}
		detail := ref.ProviderID + "/" + ref.ID
		result.picker.options = append(result.picker.options, pickerOption{
			label: "Default (no variant)", detail: detail, action: "model", model: ref, current: variant == "",
		})
		for _, item := range model.Variants {
			ref.Variant = item.ID
			result.picker.options = append(result.picker.options, pickerOption{
				label: item.ID, detail: detail, action: "model", model: ref, current: variant == item.ID,
			})
		}
		return result, nil
	})
}

func (application *app) applyPickerResult(result operationResult) {
	picker := application.picker
	if picker == nil || picker.request != result.picker.request {
		return
	}
	picker.loading, picker.submitting = false, false
	application.dirty = true
	if result.err != nil {
		picker.err = result.err.Error()
		return
	}
	switch result.kind {
	case "picker-list":
		if result.picker.append {
			// The last row is the paging action that initiated this request.
			picker.options = append(picker.options[:len(picker.options)-1], result.picker.options...)
		} else {
			picker.options = result.picker.options
			picker.selected, picker.top = 0, 0
			if picker.kind == "sessions" {
				picker.options = append([]pickerOption{{label: "+ New session", detail: application.session.Location.Directory, action: "new"}}, picker.options...)
				if len(picker.options) > 1 {
					picker.selected = 1
				}
			}
			for index, option := range picker.visible() {
				if option.current {
					picker.selected = index
					break
				}
			}
		}
		picker.next = result.picker.next
		if picker.next != "" {
			picker.options = append(picker.options, pickerOption{label: "Load more sessions…", action: "more"})
		}
	case "picker-session":
		application.closePicker()
		application.installSession(result.sessionSnapshot)
	case "picker-model":
		model := result.picker.model
		application.session.Model = &model
		application.revision++
		application.detailsRefresh = time.Now()
		application.status = "Model selected: " + model.ProviderID + "/" + model.ID
		if model.Variant != "" {
			application.status += "#" + model.Variant
		}
		application.closePicker()
	}
}

func (application *app) selectPicker() {
	picker := application.picker
	options := picker.visible()
	if picker.loading || !picker.searchAt.IsZero() || len(options) == 0 {
		return
	}
	picker.selected = max(0, min(picker.selected, len(options)-1))
	option := options[picker.selected]
	if option.action == "more" {
		application.loadPicker(true)
		return
	}
	if option.action == "session" && option.sessionID == application.session.ID {
		application.closePicker()
		return
	}
	picker.submitting = true
	session := application.session
	if option.action == "model" {
		application.pickerRequest("picker-model", func(ctx context.Context) (operationResult, error) {
			err := application.api.switchModel(ctx, session.ID, option.model)
			return operationResult{picker: &pickerResult{model: option.model}}, err
		})
		return
	}
	application.pickerRequest("picker-session", func(ctx context.Context) (operationResult, error) {
		id := option.sessionID
		if option.action == "new" {
			created, err := application.api.createSession(ctx, session.Location.Directory)
			if err != nil {
				return operationResult{}, err
			}
			id = created.ID
		}
		snapshot, err := application.api.snapshot(ctx, id, 10)
		return operationResult{sessionSnapshot: snapshot}, err
	})
}

func (application *app) handlePickerInput(event inputEvent) {
	picker := application.picker
	application.dirty = true
	if event.key == "\x11" {
		application.running = false
		return
	}
	if picker.submitting {
		return // Wait for the server's acknowledgement before accepting more input.
	}
	switch event.key {
	case "<Esc>", "\x03":
		application.closePicker()
		return
	case "<Enter>":
		application.selectPicker()
		return
	case "\x12": // Ctrl-R retries/reloads the catalog.
		picker.searchAt = time.Time{}
		application.loadPicker(false)
		return
	case "<Up>", "\x10":
		picker.selected--
	case "<Down>", "\x0e":
		picker.selected++
	case "<PageUp>":
		picker.selected -= max(1, application.rows-4)
	case "<PageDown>":
		picker.selected += max(1, application.rows-4)
	default:
		before := picker.query()
		if event.paste != "" {
			if err := picker.input.Insert(event.paste); err != nil {
				picker.err = err.Error()
			}
		} else if result := picker.input.Handle(event.key); result.Err != nil {
			picker.err = result.Err.Error()
		}
		if picker.query() != before {
			picker.selected, picker.top = 0, 0
			if picker.kind == "sessions" {
				if picker.cancel != nil {
					picker.cancel()
				}
				application.pickerSequence++
				picker.request = application.pickerSequence
				picker.options, picker.next, picker.err = nil, "", ""
				picker.loading = true
				picker.searchAt = time.Now().Add(200 * time.Millisecond)
			}
		}
	}
	picker.selected = max(0, min(picker.selected, len(picker.visible())-1))
}

func (application *app) tickPicker(now time.Time) {
	picker := application.picker
	if picker != nil && !picker.searchAt.IsZero() && !now.Before(picker.searchAt) {
		picker.searchAt = time.Time{}
		application.loadPicker(false)
	}
}

func (application *app) activeInput() *readline.Line {
	if application.deleteConfirmation != nil {
		return application.deleteConfirmation.input
	}
	if application.picker != nil {
		return application.picker.input
	}
	return application.input
}

func (picker *picker) rows(height int) []displayRow {
	options := picker.visible()
	if len(options) == 0 {
		text := "No matches"
		if picker.loading {
			text = "Loading…"
		} else if picker.err != "" {
			text = "Could not load choices; Ctrl-R retries"
		}
		return []displayRow{{text: text, style: dimStyle}}
	}
	picker.selected = max(0, min(picker.selected, len(options)-1))
	picker.top = max(0, min(picker.top, len(options)-height))
	if picker.selected < picker.top {
		picker.top = picker.selected
	} else if picker.selected >= picker.top+height {
		picker.top = picker.selected - height + 1
	}
	var rows []displayRow
	for index := picker.top; index < min(len(options), picker.top+height); index++ {
		option := options[index]
		prefix, style := "  ", bodyStyle
		if index == picker.selected {
			prefix, style = "› ", selectedStyle
		}
		label := option.label
		if option.current {
			label += " [current]"
		}
		if option.detail != "" {
			label += " · " + option.detail
		}
		rows = append(rows, displayRow{text: prefix + label, style: style})
	}
	return rows
}
