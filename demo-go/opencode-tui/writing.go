package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"st-go/tui/appbus"
	"st-go/tui/writing"
)

type writingTask struct {
	order    uint64
	ctx      context.Context
	request  writing.Message
	started  bool
	document string
	receiver string
	cancel   context.CancelFunc
	latest   writing.Message
	finished bool
}
type writingBridge struct {
	app              *app
	bus              *appbus.Client
	project, session string
	tasks            map[string]*writingTask
	documents        map[string]string
	updates          chan writing.Message
	lastHello        time.Time
	serial           uint64
}

func newWritingBridge(application *app, token, project, channel string) (*writingBridge, error) {
	if channel == "" {
		channel = writing.Channel(project)
	}
	bus, err := appbus.New(application.out, token, channel)
	if err != nil {
		return nil, err
	}
	return &writingBridge{app: application, bus: bus, project: project, session: application.session.ID, tasks: make(map[string]*writingTask), documents: make(map[string]string), updates: make(chan writing.Message, 16)}, nil
}
func (b *writingBridge) publish(m writing.Message) {
	m.Version = writing.Version
	if err := b.bus.Publish(m); err != nil {
		b.app.status = err.Error()
		b.app.dirty = true
	}
}
func (b *writingBridge) hello(to string) {
	b.publish(writing.Message{Type: "ready", To: to, Project: b.project, Session: b.session})
}
func (b *writingBridge) tick(now time.Time) {
	b.startPending()
	if err := b.bus.Tick(now); err != nil {
		b.app.status = err.Error()
	}
	if now.Sub(b.lastHello) > 2*time.Second {
		b.lastHello = now
		b.hello("")
	}
	for i := 0; i < 16; i++ {
		select {
		case update := <-b.updates:
			task := b.tasks[update.RequestID]
			if task == nil {
				continue
			}
			if update.Type == "settled" {
				task.finished = true
				continue
			}
			update.To = task.receiver
			if update.Session != "" {
				b.documents[task.document] = update.Session
			}
			task.latest = update
			b.publish(update)
			b.app.status = "Writing: " + update.Type + " · " + update.Session
			b.app.dirty = true
			if update.Session == b.app.session.ID {
				b.app.requestSync()
			}
		default:
			return
		}
	}
}
func (b *writingBridge) close() {
	for _, task := range b.tasks {
		task.cancel()
	}
	_ = b.bus.Close()
}
func (b *writingBridge) receive(control string) {
	frame, err := b.bus.Receive(control)
	if err != nil {
		b.app.status = err.Error()
		b.app.dirty = true
		return
	}
	if frame == nil || frame.Sender == b.bus.ID {
		return
	}
	var request writing.Message
	if json.Unmarshal(frame.Data, &request) != nil || request.Version != writing.Version || request.To != "" && request.To != b.bus.ID {
		return
	}
	if request.Type == "hello" {
		b.hello(frame.Sender)
		return
	}
	if request.To != b.bus.ID {
		return
	}
	if task := b.tasks[request.RequestID]; task != nil {
		if request.Type == "cancel" {
			task.cancel()
			if !task.started && !task.finished {
				task.finished = true
				task.latest.Type = "cancelled"
				update := task.latest
				update.To = task.receiver
				b.publish(update)
			}
		}
		if request.Type == "status" || request.Type == "suggest" {
			task.receiver = frame.Sender
			update := task.latest
			update.To = frame.Sender
			b.publish(update)
		}
		return
	}
	resume := request.Type == "status" && request.Session != ""
	if request.Type == "status" && !resume {
		b.publish(writing.Message{Type: "unknown", To: frame.Sender, RequestID: request.RequestID})
		return
	}
	if request.Type != "suggest" && !resume || request.To != b.bus.ID {
		return
	}
	fail := func(err error) {
		b.publish(writing.Message{Type: "failed", To: frame.Sender, RequestID: request.RequestID, Placeholder: request.Placeholder, Error: err.Error()})
	}
	queued := 0
	for _, task := range b.tasks {
		if !task.finished {
			queued++
		}
	}
	if queued >= 16 {
		fail(fmt.Errorf("writing request queue is full"))
		return
	}
	if len(b.tasks) >= 256 {
		for id, task := range b.tasks {
			if task.finished {
				delete(b.tasks, id)
				break
			}
		}
	}
	if len(request.Draft) > writing.MaxResponse || len(request.Instruction) > 16384 {
		fail(fmt.Errorf("writing input too large"))
		return
	}
	ctx, cancel := context.WithCancel(b.app.ctx)
	b.serial++
	b.tasks[request.RequestID] = &writingTask{order: b.serial, ctx: ctx, request: request, document: request.Document, receiver: frame.Sender, cancel: cancel, latest: writing.Message{Type: "pending", RequestID: request.RequestID, Placeholder: request.Placeholder}}
	b.startPending()
}

func (b *writingBridge) startPending() {
	active := 0
	documents := make(map[string]bool)
	var queued []*writingTask
	for _, task := range b.tasks {
		if task.started && !task.finished {
			active++
			documents[task.document] = true
		}
		if !task.started && !task.finished {
			queued = append(queued, task)
		}
	}
	sort.Slice(queued, func(i, j int) bool { return queued[i].order < queued[j].order })
	for _, task := range queued {
		if active >= 2 {
			return
		}
		if task.started || task.finished || documents[task.document] {
			continue
		}
		task.started = true
		active++
		documents[task.document] = true
		request := task.request
		if session := b.documents[task.document]; session != "" {
			request.Session = session
		}
		go func() {
			defer task.cancel()
			b.generate(task.ctx, request)
			// Only release this document after deferred interruption/stream
			// cleanup completes, otherwise a retry could be interrupted by it.
			select {
			case b.updates <- writing.Message{Type: "settled", RequestID: request.RequestID}:
			case <-b.app.ctx.Done():
			}
		}()
	}
}

func (b *writingBridge) generate(ctx context.Context, request writing.Message) {
	update := writing.Message{RequestID: request.RequestID, Placeholder: request.Placeholder, Project: b.project}
	emit := func(kind string) bool {
		update.Type = kind
		select {
		case b.updates <- update:
			return true
		case <-b.app.ctx.Done():
			return false
		}
	}
	fail := func(err error) {
		update.Error = err.Error()
		if ctx.Err() != nil {
			emit("cancelled")
		} else {
			emit("failed")
		}
	}
	prompt, err := writing.ReadContext(request, b.project)
	if err != nil {
		fail(err)
		return
	}
	// Bind a dedicated writing conversation to the document. Browsing another
	// session never changes this destination; govim persists it across restarts.
	var fork struct {
		Data sessionInfo `json:"data"`
	}
	fork.Data.ID = request.Session
	if fork.Data.ID == "" {
		err = b.app.api.request(ctx, http.MethodPost, "/api/session/"+url.PathEscape(b.session)+"/fork", map[string]any{}, &fork)
	} else {
		fork.Data, err = b.app.api.getSession(ctx, fork.Data.ID)
	}
	if err != nil {
		fail(err)
		return
	}
	update.Session = fork.Data.ID
	if update.Session == "" {
		fail(fmt.Errorf("server returned an empty writing session"))
		return
	}
	if !emit("pending") {
		return
	}
	completed := false
	defer func() {
		if !completed && (ctx.Err() != nil || request.Type != "status") {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = b.app.api.interrupt(cleanup, fork.Data.ID)
		}
	}()
	events := make(chan apiEvent, 128)
	streamCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		for streamCtx.Err() == nil {
			_ = b.app.api.subscribe(streamCtx, func(event apiEvent) error {
				var d eventData
				if json.Unmarshal(event.Data, &d) == nil && d.SessionID == fork.Data.ID {
					select {
					case events <- event:
					case <-streamCtx.Done():
						return streamCtx.Err()
					}
				}
				return nil
			})
			select {
			case <-time.After(time.Second):
			case <-streamCtx.Done():
				return
			}
		}
	}()
	messageID := "msg_" + request.RequestID
	if request.Type != "status" {
		if err = b.app.api.prompt(ctx, fork.Data.ID, prompt, messageID); err != nil {
			// Ambiguous acknowledgements are reconciled through history. Never send
			// a second prompt as a transport retry.
			if rejectedPrompt(err) {
				fail(err)
				return
			}
		}
	}
	parts := make(map[string]string)
	thoughts := make(map[string]string)
	last := time.Time{}
	dirty := false
	started := time.Now()
	seen := false
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	join := func(values map[string]string) string {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var all []string
		for _, key := range keys {
			all = append(all, values[key])
		}
		return strings.Join(all, "\n\n")
	}
	refresh := func() (bool, error) {
		snapshot, err := b.app.api.snapshot(ctx, fork.Data.ID, 100)
		if err != nil {
			return false, err
		}
		current := false
		outcome := ""
		newParts, newThoughts := make(map[string]string), make(map[string]string)
		for _, message := range snapshot.messages {
			if message.ID == messageID {
				current = true
				seen = true
				continue
			}
			if current && message.Type == "user" {
				break
			}
			if current && message.Type == "idle" {
				outcome = message.Outcome
				break
			}
			if !current || message.Type != "assistant" {
				continue
			}
			for ordinal, content := range message.Content {
				key := fmt.Sprintf("%s:%08d", message.ID, ordinal)
				if content.Type == "text" {
					newParts[key] = content.Text
				}
				if content.Type == "reasoning" {
					newThoughts[key] = content.Text
				}
			}
		}
		if current {
			parts, thoughts = newParts, newThoughts
			dirty = true
		}
		if len(snapshot.permissions) > 0 {
			update.Error = "Writing session awaits permission; open " + fork.Data.ID + " in opencode-tui"
		} else {
			update.Error = ""
		}
		if outcome == "failed" || outcome == "interrupted" {
			return false, fmt.Errorf("writing execution %s", outcome)
		}
		return current && outcome == "succeeded", nil
	}
	finish := func() {
		update.Text = join(parts)
		update.Reasoning = join(thoughts)
		if len(update.Text)+len(update.Reasoning) > writing.MaxResponse {
			fail(fmt.Errorf("writing response exceeds size limit"))
			return
		}
		prediction, err := writing.Prediction(update.Text, request.RequestID)
		if err != nil {
			fail(err)
			return
		}
		update.Prediction = prediction
		completed = true
		emit("result")
	}
	for {
		select {
		case <-ctx.Done():
			fail(ctx.Err())
			return
		case event := <-events:
			var d eventData
			if json.Unmarshal(event.Data, &d) != nil {
				continue
			}
			key := fmt.Sprintf("%s:%08d", d.AssistantMessageID, d.Ordinal)
			switch event.Type {
			case "session.text.delta":
				parts[key] += d.Delta
				dirty = true
			case "session.text.ended":
				parts[key] = d.Text
				dirty = true
			case "session.reasoning.delta":
				thoughts[key] += d.Delta
				dirty = true
			case "session.reasoning.ended":
				thoughts[key] = d.Text
				dirty = true
			case "session.execution.succeeded":
				if done, err := refresh(); err == nil && done {
					finish()
					return
				}
			case "session.execution.failed", "session.execution.interrupted":
				fail(fmt.Errorf("writing execution stopped"))
				return
			}
		case now := <-ticker.C:
			if request.Type == "status" && !seen && now.Sub(started) > 15*time.Second {
				update.Error = "Request not found in saved session; explicit retry required"
				emit("unknown")
				return
			}
			if now.Sub(started) > 20*time.Minute {
				fail(fmt.Errorf("writing request timed out; inspect session %s before retrying", update.Session))
				return
			}
			if now.Sub(last) >= 3*time.Second {
				last = now
				if done, err := refresh(); err == nil && done {
					finish()
					return
				} else if err != nil {
					update.Error = err.Error()
					if strings.HasPrefix(err.Error(), "writing execution ") {
						fail(err)
						return
					}
				}
			}
			if dirty {
				update.Text = join(parts)
				update.Reasoning = join(thoughts)
				if len(update.Text)+len(update.Reasoning) > writing.MaxResponse {
					fail(fmt.Errorf("writing response exceeds size limit"))
					return
				}
				emit("progress")
				dirty = false
			}
		}
	}
}
