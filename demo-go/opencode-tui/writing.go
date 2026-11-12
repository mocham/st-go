package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
	opened   bool
}
type writingBridge struct {
	app              *app
	bus              *appbus.Client
	project, session string
	tasks            map[string]*writingTask
	updates          chan writing.Message
	lastHello        time.Time
	serial           uint64
	peers            map[string]writingPeer
	subscribed       time.Time
	connection       string
}

type writingPeer struct {
	seen    time.Time
	project string
}

func (application *app) subscribeWriting(channel string) error {
	if !appbus.ValidChannel(channel) {
		return fmt.Errorf("usage: /Subscribe <Channel> (1–96 letters, digits, . _ : -)")
	}
	if application.session.ID == "" || application.attachTarget != "" {
		return fmt.Errorf("select a session before /Subscribe %s", channel)
	}
	project, err := writing.Workdir()
	if err != nil {
		return err
	}
	if b := application.writer; b != nil {
		if b.project == project && b.bus.Channel == channel && b.session == application.session.ID {
			b.hello("")
			application.status = b.connection + " · " + project
			return nil
		}
		for _, task := range b.tasks {
			if !task.finished {
				return fmt.Errorf("finish or cancel writing requests before changing subscription")
			}
		}
	}
	bridge, err := newWritingBridge(application, os.Getenv("ST_GO_APPBUS_TOKEN"), project, channel)
	if err != nil {
		return fmt.Errorf("terminal channels unavailable; run both applications in an updated st instance")
	}
	if application.writer != nil {
		if application.writer.bus.Channel == channel {
			// Preserve the terminal identity when rebinding the same channel.
			bridge.bus = application.writer.bus
		}
		application.writer.close()
	}
	application.writer = bridge
	bridge.updateConnection(time.Now())
	bridge.hello("")
	application.scroll, application.sidebarFocus = 0, ""
	return nil
}

func newWritingBridge(application *app, token, project, channel string) (*writingBridge, error) {
	bus, err := appbus.New(application.out, token, channel)
	if err != nil {
		return nil, err
	}
	return &writingBridge{app: application, bus: bus, project: project, session: application.session.ID, tasks: make(map[string]*writingTask), updates: make(chan writing.Message, 16), peers: make(map[string]writingPeer), subscribed: time.Now()}, nil
}

func (b *writingBridge) updateConnection(now time.Time) {
	label := "waiting for :Subscribe " + b.bus.Channel + " in govim"
	count, mismatch := 0, ""
	for id, peer := range b.peers {
		if now.Sub(peer.seen) > 6*time.Second {
			delete(b.peers, id)
			continue
		}
		if peer.project == b.project {
			count++
		} else {
			mismatch = peer.project
		}
	}
	if count > 0 {
		label = fmt.Sprintf("connected · %d govim", count)
	} else if mismatch != "" {
		label = "workdir mismatch: govim uses " + mismatch
	}
	if !b.bus.Ready {
		label = "subscribing"
		if now.Sub(b.subscribed) > 3*time.Second {
			label = "terminal not responding; restart updated st"
		}
	}
	label = "Writing [" + b.bus.Channel + "] " + label
	if b.connection != label {
		b.connection = label
		b.app.status, b.app.dirty = label, true
	}
}

// Keep writing activity visible even when the user browses another session.
// Every session-bearing row can open the normal response/model/permission UI.
func (b *writingBridge) rows(width int) []displayRow {
	rows := []displayRow{}
	for _, line := range wrapText(b.connection, width) {
		rows = append(rows, displayRow{text: line, style: assistantStyle})
	}
	for _, line := range wrapText("Workdir: "+b.project, width) {
		rows = append(rows, displayRow{text: line, style: workdirStyle})
	}
	for _, line := range wrapText("Subscribed session: "+b.session+" · click to open", width) {
		rows = append(rows, displayRow{text: line, style: sessionNameStyle, sessionID: b.session})
	}
	var tasks []*writingTask
	for _, task := range b.tasks {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].finished != tasks[j].finished {
			return !tasks[i].finished
		}
		return tasks[i].order > tasks[j].order
	})
	for _, task := range tasks[:min(len(tasks), 4)] {
		label := writing.StateLabel(task.latest.Type)
		if !task.started && !task.finished {
			label = "queued"
		}
		text := task.document + " · " + label
		style := waitingSessionStyle
		if task.latest.Type == "result" {
			style = assistantStyle
		}
		if task.latest.Error != "" {
			text += " · " + task.latest.Error
			style = warningStyle
		}
		if task.latest.Session != "" {
			text += " · click to open"
		}
		for _, line := range wrapText(text, width) {
			rows = append(rows, displayRow{text: line, style: style, sessionID: task.latest.Session})
		}
	}
	return append(rows, displayRow{text: "/models · /variants: writing session model", style: dimStyle}, displayRow{})
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
	b.updateConnection(now)
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
				b.app.dirty = true
				continue
			}
			update.To = task.receiver
			if update.Type == "submitted" {
				if b.app.session.ID == b.session {
					id := "msg_" + update.RequestID
					if _, exists := b.app.messageIndex[id]; !exists {
						b.app.messages = append(b.app.messages, chatMessage{ID: id, Role: "user", Text: update.Text, Rich: update.Text, Pending: true})
						b.app.rebuildIndex()
						if b.app.followPreview {
							b.app.selectLatestPreview()
						}
					}
				}
				b.app.dirty = true
				continue
			}
			task.latest = update
			b.publish(update)
			b.app.status = "Writing " + task.document + ": " + writing.StateLabel(update.Type)
			if update.Error != "" {
				b.app.status += " · " + update.Error
			}
			b.app.dirty = true
			if update.Session != "" && !task.opened {
				task.opened = true
				b.app.filesMode = false
				if b.app.session.ID != update.Session {
					b.app.closePicker()
					b.app.attachSession(update.Session)
				}
			}
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
	defer b.updateConnection(time.Now())
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
		b.peers[frame.Sender] = writingPeer{seen: time.Now(), project: request.Project}
		b.hello(frame.Sender)
		return
	}
	if request.To != b.bus.ID {
		return
	}
	if request.Project != "" && request.Project != b.project {
		b.publish(writing.Message{Type: "failed", To: frame.Sender, RequestID: request.RequestID, Error: "workdir mismatch: opencode-tui uses " + b.project})
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
				b.app.dirty = true
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
	resume := request.Type == "status"
	if resume && request.Session != "" && request.Session != b.session {
		b.publish(writing.Message{Type: "unknown", To: frame.Sender, RequestID: request.RequestID, Error: "Open session " + request.Session + " and /Subscribe " + b.bus.Channel + " to recover this request"})
		return
	}
	if request.Type != "suggest" && !resume || request.To != b.bus.ID {
		return
	}
	fail := func(err error) {
		b.publish(writing.Message{Type: "failed", To: frame.Sender, RequestID: request.RequestID, Placeholder: request.Placeholder, Error: err.Error()})
		b.app.status, b.app.dirty = "Writing request failed: "+err.Error(), true
	}
	if b.session == "" {
		fail(fmt.Errorf("select an OpenCode session, then /Subscribe %s", b.bus.Channel))
		return
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
	latest := writing.Message{Type: "pending", To: frame.Sender, RequestID: request.RequestID, Placeholder: request.Placeholder, Session: b.session}
	b.tasks[request.RequestID] = &writingTask{order: b.serial, ctx: ctx, request: request, document: request.Document, receiver: frame.Sender, cancel: cancel, latest: latest}
	b.publish(latest) // Acknowledge receipt before context loading or any HTTP call.
	b.app.status, b.app.dirty = "Writing request received: "+request.Document, true
	b.app.scroll, b.app.sidebarFocus = 0, ""
	b.startPending()
}

func (b *writingBridge) startPending() {
	active := 0
	var queued []*writingTask
	for _, task := range b.tasks {
		if task.started && !task.finished {
			active++
		}
		if !task.started && !task.finished {
			queued = append(queued, task)
		}
	}
	sort.Slice(queued, func(i, j int) bool { return queued[i].order < queued[j].order })
	for _, task := range queued {
		if active >= 1 {
			return
		}
		if task.started || task.finished {
			continue
		}
		task.started = true
		active++
		request := task.request
		go func() {
			defer task.cancel()
			b.generate(task.ctx, request)
			// Only release this session after deferred interruption
			// cleanup completes, otherwise a retry could be interrupted by it.
			select {
			case b.updates <- writing.Message{Type: "settled", RequestID: request.RequestID}:
			case <-b.app.ctx.Done():
			}
		}()
	}
}

// A writing request shares an ordinary session with typed prompts. Cancel its
// queued inbox item, or interrupt only while it is still the current turn.
func (b *writingBridge) cancelRequest(ctx context.Context, messageID string) {
	if items, err := b.app.api.inbox(ctx, b.session); err == nil {
		for _, item := range items {
			if item.ID == messageID {
				_ = b.app.api.request(ctx, http.MethodDelete, "/api/session/"+url.PathEscape(b.session)+"/inbox/"+url.PathEscape(item.ID), nil, nil)
				return
			}
		}
	}
	messages, _, err := b.app.api.promptHistory(ctx, b.session, 1)
	if err != nil {
		return
	}
	for i := len(messages) - 1; i >= 0; i-- {
		switch messages[i].Type {
		case "idle":
			return
		case "user":
			if messages[i].ID == messageID {
				_ = b.app.api.interrupt(ctx, b.session)
			}
			return
		}
	}
}

func (b *writingBridge) generate(ctx context.Context, request writing.Message) {
	update := writing.Message{RequestID: request.RequestID, Placeholder: request.Placeholder, Project: b.project, Session: b.session}
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
	var prompt string
	if request.Type != "status" {
		var err error
		prompt, err = writing.ReadContext(request, b.project)
		if err != nil {
			fail(err)
			return
		}
	}
	// Recovery reads the existing server turn; it needs neither fresh context
	// nor a re-upload of a previous document excerpt.
	// /Subscribe binds the normal, currently selected session. Its server-side
	// model/variant settings apply to these prompts just as to typed prompts.
	if !emit("pending") {
		return
	}
	completed := false
	messageID := "msg_" + request.RequestID
	defer func() {
		if !completed && ctx.Err() != nil && b.app.ctx.Err() == nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			b.cancelRequest(cleanup, messageID)
		}
	}()
	if request.Type != "status" {
		update.Text = prompt
		if !emit("submitted") {
			return
		}
		update.Text = ""
		if err := b.app.api.prompt(ctx, b.session, prompt, messageID); err != nil {
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
		snapshot, err := b.app.api.snapshot(ctx, b.session, 100)
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
			update.Error = "Session awaits permission; open " + b.session + " in opencode-tui"
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
			if dirty || update.Error != "" {
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
