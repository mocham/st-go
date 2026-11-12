package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Watch raw bytes, including SSE keepalive comments. Quiet model/tool work is
// healthy while the server keeps the stream alive; a half-open socket is not.
type streamWatch struct {
	mu            sync.Mutex
	timer         *time.Timer
	deadline      time.Time
	idle          time.Duration
	live, stopped bool
	cancel        context.CancelCauseFunc
}

func newStreamWatch(connect, idle time.Duration, cancel context.CancelCauseFunc) *streamWatch {
	w := &streamWatch{deadline: time.Now().Add(connect), idle: idle, cancel: cancel}
	w.timer = time.AfterFunc(connect, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.stopped {
			return
		}
		if remaining := time.Until(w.deadline); remaining > 0 {
			w.timer.Reset(remaining)
			return
		}
		message := "Timed out connecting to OpenCode event stream"
		if w.live {
			message = "OpenCode event stream stalled (no data or keepalive)"
		}
		w.cancel(fmt.Errorf("%s", message))
	})
	return w
}

func (w *streamWatch) connected() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.live = true
	w.deadline = time.Now().Add(w.idle)
}

func (w *streamWatch) touch() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.live {
		w.deadline = time.Now().Add(w.idle)
	}
}

func (w *streamWatch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	w.timer.Stop()
}

type activityReader struct {
	io.Reader
	activity func()
}

func (reader activityReader) Read(data []byte) (int, error) {
	n, err := reader.Reader.Read(data)
	if n > 0 {
		reader.activity()
	}
	return n, err
}

func (application *app) pendingPrompts() bool {
	for _, message := range application.messages {
		if message.Role == "user" && message.Pending {
			return true
		}
	}
	return false
}

func (application *app) replaceSessionActivity(active map[string]bool) {
	application.sessionActivity = make(map[string]bool, len(active))
	for id, busy := range active {
		if busy && !application.deleted[id] {
			application.sessionActivity[id] = true
		}
	}
	application.activityRevision++
	application.dirty = true
}

// Track activity before filtering messages to the selected session, so a folded
// session remains highlighted while the server works on it. Snapshot refreshes
// repair missed events, but must not overwrite newer activity from any session.
func (application *app) recordSessionActivity(event apiEvent, id string) {
	if id == "" || application.deleted[id] {
		return
	}
	busy := false
	switch event.Type {
	case "session.execution.started", "session.step.started", "session.text.started", "session.reasoning.started":
		busy = true
	case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
	case "session.status":
		var data struct {
			Status struct{ Type string } `json:"status"`
		}
		if json.Unmarshal(event.Data, &data) != nil || data.Status.Type == "" {
			return
		}
		busy = data.Status.Type != "idle"
	default:
		return
	}
	application.sessionActivity[id] = busy
	application.activityRevision++
	application.dirty = true
}

func (application *app) tickConnection(now time.Time) {
	if application.api == nil {
		return
	}
	if !application.nextSync.IsZero() && !now.Before(application.nextSync) {
		application.requestSync()
	}
	if !application.detailsRefresh.IsZero() && !now.Before(application.detailsRefresh) {
		application.requestDetails()
	}
}

func (application *app) syncInterval() time.Duration {
	if application.busy || application.pendingPrompts() || !application.streamConnected {
		return 5 * time.Second
	}
	return 30 * time.Second
}

func (application *app) applyStream(item streamItem) {
	if item.event != nil {
		application.applyEvent(*item.event)
		return
	}
	if item.err == nil {
		return
	}
	application.streamConnected = false
	application.status = "Event stream reconnecting: " + item.err.Error()
	application.dirty = true
	application.requestSync()
}

func (application *app) applyPromptResult(result operationResult) {
	index, ok := application.messageIndex[result.requestID]
	if !ok {
		return
	}
	message := &application.messages[index]
	switch {
	case message.Acknowledged:
		// Durable inbox/history evidence wins over a late HTTP timeout/error.
		// A later server cancellation is authoritative too.
		message.Uncertain = false
	case result.err == nil:
		message.Acknowledged = true
		message.Failed, message.Uncertain = false, false
		application.status = "Prompt accepted"
	case rejectedPrompt(result.err):
		message.Pending, message.Failed, message.Uncertain = false, true, false
		if application.activePrompt == result.requestID || application.activePrompt == "" {
			application.busy = result.wasBusy
		}
		application.status = result.err.Error()
	default:
		message.Uncertain = true
		application.status = "Send status unknown; checking server: " + result.err.Error()
	}
	application.nextSync = time.Now()
	application.sessionActivity[application.session.ID] = application.busy || application.pendingPrompts()
	application.activityRevision++
	application.queuePreview()
	application.dirty = true
}

func (application *app) mergeInbox(items []inboxItem) {
	for _, item := range items {
		if item.Type != "user" {
			continue
		}
		if index, ok := application.messageIndex[item.ID]; ok {
			message := &application.messages[index]
			message.Acknowledged = true
			message.Failed, message.Uncertain = false, false
		} else {
			application.messages = append(application.messages, chatMessage{ID: item.ID, Role: "user", Text: item.Payload.Text, Pending: true, Acknowledged: true})
			application.rebuildIndex()
		}
	}
}
