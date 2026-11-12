package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"st-go/tui/readline"
)

const promptPrefix = "› "

type chatMessage struct {
	ID           string
	Role         string
	Text         string
	Rich         string
	Aux          string
	Parts        map[int]string
	PromptID     string
	Pending      bool
	Failed       bool
	Acknowledged bool
	Uncertain    bool
	Tools        []messageContent
	Order        []responsePart
	Files        []messageAttachment
	ChangedFiles []string
}

type streamItem struct {
	event *apiEvent
	err   error
}

type operationResult struct {
	sessionSnapshot
	kind             string
	err              error
	requestID        string
	wasBusy          bool
	sessionID        string
	epoch            uint64
	revision         uint64
	picker           *pickerResult
	catalog          *catalogResult
	attachToken      uint64
	details          *sessionDetails
	file             *fileResult
	export           *exportResult
	activityRevision uint64
}

type app struct {
	api     *apiClient
	session sessionInfo
	out     io.Writer
	fd      int
	preview *richPreview

	ctx    context.Context
	cancel context.CancelFunc

	messages       []chatMessage
	messageIndex   map[string]int
	permissions    []permissionRequest
	permissionBusy bool
	input          *readline.Line
	history        []string
	stream         chan streamItem
	operations     chan operationResult
	signals        chan os.Signal

	rows, cols               int
	scroll                   int
	selectedPrompt           string
	activePrompt             string
	followPreview            bool
	busy                     bool
	running                  bool
	dirty                    bool
	status                   string
	streamConnected          bool
	syncing                  bool
	lastDraw                 time.Time
	previewGeneration        string
	previewKey               string
	responseScroll           int
	sidebarHits              map[int]sidebarHit
	sessions                 []sessionInfo
	promptLimit              int
	historyCursor            string
	sidebarFocus             string
	catalogLoading           bool
	catalogSequence          uint64
	catalogError             string
	catalogRefresh           time.Time
	attachSequence           uint64
	attachTarget             string
	attachCancel             context.CancelFunc
	paintedLayout            paneLayout
	paintedRows, paintedCols int
	sessionEpoch             uint64
	revision                 uint64
	picker                   *picker
	pickerSequence           uint64
	nextSync                 time.Time
	detailsRefresh           time.Time
	detailsLoading           bool
	details                  sessionDetails
	usage                    *contextUsage
	sending                  map[string]<-chan struct{}
	deleting                 map[string]bool
	deleted                  map[string]bool
	deleteHits               map[int]string
	deleteConfirmation       *deleteConfirmation
	exportHits               map[int]string
	exportDialog             *sessionExportDialog
	exportSequence           uint64
	sessionActivity          map[string]bool
	activityRevision         uint64

	filesMode                 bool
	promptScroll, filesScroll int
	fileSelection             *fileSelection
	fileSequence              uint64
	fileCancel                context.CancelFunc
}

func newApp(client *apiClient, session sessionInfo, messages []apiMessage, permissions []permissionRequest, output io.Writer, fd int, rich *richPreview) *app {
	ctx, cancel := context.WithCancel(context.Background())
	application := &app{
		api: client, session: session, out: output, fd: fd, preview: rich,
		ctx: ctx, cancel: cancel, input: readline.New(promptPrefix, len([]rune(promptPrefix)), 64<<10),
		stream: make(chan streamItem, 1024), operations: make(chan operationResult, 32), signals: make(chan os.Signal, 16),
		messageIndex: make(map[string]int), followPreview: true,
		promptLimit: 10, sessions: []sessionInfo{session}, sidebarFocus: session.ID,
		rows: 24, cols: 80, running: true, dirty: true, status: "Ready",
		sending: make(map[string]<-chan struct{}), deleting: make(map[string]bool), deleted: make(map[string]bool),
		sessionActivity: make(map[string]bool),
	}
	application.replaceMessages(messages)
	application.permissions = append([]permissionRequest(nil), permissions...)
	application.selectLatestPreview()
	return application
}

func convertMessages(messages []apiMessage) []chatMessage {
	converted := make([]chatMessage, 0, len(messages))
	promptID := ""
	for _, message := range messages {
		switch message.Type {
		case "user":
			promptID = message.ID
			converted = append(converted, chatMessage{ID: message.ID, Role: "user", Text: message.Text, Rich: message.Text, Acknowledged: true, Files: message.Files})
		case "assistant":
			item := chatMessage{ID: message.ID, Role: "assistant", PromptID: promptID, Parts: make(map[int]string), ChangedFiles: message.Snapshot.Files}
			for ordinal, content := range message.Content {
				switch content.Type {
				case "text":
					item.Parts[ordinal] = content.Text
					item.Order = append(item.Order, responsePart{ordinal: ordinal})
				case "tool":
					if content.ID == "" {
						content.ID = fmt.Sprintf("part-%d", ordinal)
					}
					item.Tools = append(item.Tools, content)
					item.Order = append(item.Order, responsePart{toolID: content.ID})
				}
			}
			rebuildMessage(&item)
			converted = append(converted, item)
		}
	}
	return converted
}

func (application *app) rebuildIndex() {
	application.messageIndex = make(map[string]int, len(application.messages))
	for index := range application.messages {
		application.messageIndex[application.messages[index].ID] = index
	}
}

func (application *app) replaceMessages(messages []apiMessage) {
	selected := application.selectedPrompt
	application.updateUsage(messages)
	previous := application.messages
	application.messages = convertMessages(messages)
	application.rebuildIndex()
	application.activePrompt = ""
	for _, message := range application.messages {
		if message.Role == "user" {
			application.activePrompt = message.ID
		}
	}
	// Queued inputs are not in the delivered message snapshot yet.
	for _, message := range previous {
		if _, found := application.messageIndex[message.ID]; !found && message.Role == "user" && (message.Pending || message.Failed) {
			application.messages = append(application.messages, message)
		}
	}
	application.rebuildIndex()
	if application.followPreview {
		application.selectLatestPreview()
	} else if _, ok := application.messageIndex[application.selectedPrompt]; !ok {
		application.selectLatestPreview()
	}
	if selected != application.selectedPrompt {
		application.resetFiles()
	}
	application.dirty = true
}

func (application *app) ensureAssistant(messageID string) *chatMessage {
	promptID := application.activePrompt
	if index, ok := application.messageIndex[messageID]; ok {
		promptID = application.messages[index].PromptID
	}
	if index, ok := application.messageIndex[promptID]; ok {
		message := &application.messages[index]
		message.Acknowledged = true
		message.Pending, message.Failed, message.Uncertain = false, false, false
	}
	if index, ok := application.messageIndex[messageID]; ok {
		return &application.messages[index]
	}
	application.messages = append(application.messages, chatMessage{ID: messageID, Role: "assistant", Parts: make(map[int]string), PromptID: application.activePrompt})
	application.messageIndex[messageID] = len(application.messages) - 1
	return &application.messages[len(application.messages)-1]
}

func (application *app) rebuildAssistant(message *chatMessage) {
	rebuildMessage(message)
}

func (application *app) promptIndexes() []int {
	indexes := make([]int, 0)
	for index := range application.messages {
		if application.messages[index].Role == "user" {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func (application *app) selectLatestPreview() {
	indexes := application.promptIndexes()
	application.selectedPrompt = ""
	if len(indexes) > 0 {
		application.selectedPrompt = application.messages[indexes[len(indexes)-1]].ID
	}
}

func (application *app) movePreview(delta int) {
	indexes := application.promptIndexes()
	for left, right := 0, len(indexes)-1; left < right; left, right = left+1, right-1 {
		indexes[left], indexes[right] = indexes[right], indexes[left]
	}
	if len(indexes) == 0 {
		application.status = "No prompts yet"
		application.dirty = true
		return
	}
	position := len(indexes) - 1
	for index, messageIndex := range indexes {
		if application.messages[messageIndex].ID == application.selectedPrompt {
			position = index
			break
		}
	}
	position = max(0, min(len(indexes)-1, position+delta))
	application.selectPrompt(application.messages[indexes[position]].ID)
	application.ensurePromptVisible()
}

func (application *app) queuePreview() {
	if application.preview == nil {
		return
	}
	key := application.session.ID + "\x00" + application.selectedPrompt
	if application.attachTarget != "" {
		key = application.attachTarget + "\x00loading"
	}
	text := application.selectedResponse()
	if application.filesMode && application.fileSelection != nil && application.attachTarget == "" {
		key += fmt.Sprintf("\x00file\x00%s\x00%d", application.fileSelection.ref.ID, application.fileSequence)
	}
	generation := key + "\x00" + text
	if generation == application.previewGeneration {
		return
	}
	application.previewGeneration = generation
	if key != application.previewKey {
		application.previewKey = key
		application.responseScroll = 0
		if err := application.preview.clear(); err != nil {
			application.status = err.Error()
		}
	}
	if selected := application.fileSelection; application.filesMode && selected != nil && selected.media != nil && application.attachTarget == "" {
		if err := application.preview.requestMedia(key, selected.media); err != nil {
			application.status = err.Error()
		}
	} else {
		application.preview.request(key, text)
	}
}

func (application *app) startEventStream() {
	go func() {
		for application.ctx.Err() == nil {
			err := application.api.subscribe(application.ctx, func(event apiEvent) error {
				copy := event
				select {
				case application.stream <- streamItem{event: &copy}:
					return nil
				case <-application.ctx.Done():
					return application.ctx.Err()
				}
			})
			if application.ctx.Err() != nil {
				return
			}
			select {
			case application.stream <- streamItem{err: err}:
			case <-application.ctx.Done():
				return
			}
			select {
			case <-time.After(time.Second):
			case <-application.ctx.Done():
				return
			}
		}
	}()
}

func (application *app) requestSync() {
	if application.syncing || application.api == nil || application.session.ID == "" {
		return
	}
	application.syncing = true
	application.nextSync = time.Now().Add(application.syncInterval())
	id, epoch, revision := application.session.ID, application.sessionEpoch, application.revision
	activityRevision := application.activityRevision
	limit := application.promptLimit
	pending := application.pendingPrompts()
	go func() {
		ctx, cancel := context.WithTimeout(application.ctx, 30*time.Second)
		defer cancel()
		snapshot, err := application.api.snapshot(ctx, id, limit)
		if err == nil && pending {
			snapshot.inbox, _ = application.api.inbox(ctx, id)
		}
		result := operationResult{sessionSnapshot: snapshot}
		result.kind, result.sessionID, result.epoch, result.revision = "sync", id, epoch, revision
		result.activityRevision = activityRevision
		result.err = err
		select {
		case application.operations <- result:
		case <-application.ctx.Done():
		}
	}()
}

func (application *app) applyEvent(event apiEvent) {
	if event.Type == "server.connected" {
		application.streamConnected = true
		application.status = "Connected"
		application.requestSync()
		application.requestCatalog()
		application.detailsRefresh = time.Now()
		application.dirty = true
		return
	}
	var data eventData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		application.status = "Event decode failed: " + err.Error()
		application.dirty = true
		return
	}
	if event.Type == "session.deleted" && data.SessionID != "" {
		application.removeSession(data.SessionID)
		return
	}
	switch event.Type {
	case "session.created", "session.deleted", "session.archived", "session.unarchived", "session.moved", "session.renamed":
		application.catalogRefresh = time.Now().Add(300 * time.Millisecond)
	}
	application.recordSessionActivity(event, data.SessionID)
	if data.SessionID != "" && data.SessionID != application.session.ID {
		return
	}
	if application.session.ID == "" || application.deleted[data.SessionID] {
		return
	}
	application.revision++
	switch event.Type {
	case "session.inbox.enqueued":
		var queued struct {
			InboxID string `json:"inboxID"`
			Item    struct {
				Type    string `json:"type"`
				Payload struct {
					Text string `json:"text"`
				} `json:"payload"`
			} `json:"item"`
		}
		if json.Unmarshal(event.Data, &queued) == nil && queued.Item.Type == "user" && queued.InboxID != "" {
			if index, exists := application.messageIndex[queued.InboxID]; exists {
				application.messages[index].Acknowledged = true
				application.messages[index].Failed, application.messages[index].Uncertain = false, false
			} else {
				application.messages = append(application.messages, chatMessage{ID: queued.InboxID, Role: "user", Text: queued.Item.Payload.Text, Pending: true, Acknowledged: true})
				application.rebuildIndex()
				if application.followPreview {
					application.selectLatestPreview()
					application.sidebarFocus = application.session.ID
				}
			}
		}
	case "session.inbox.delivered":
		var delivered struct {
			InboxID string `json:"inboxID"`
		}
		if json.Unmarshal(event.Data, &delivered) == nil {
			if index, exists := application.messageIndex[delivered.InboxID]; exists && application.messages[index].Role == "user" {
				application.messages[index].Pending = false
				application.messages[index].Acknowledged = true
				application.messages[index].Failed, application.messages[index].Uncertain = false, false
				application.activePrompt = delivered.InboxID
			}
		}
	case "session.inbox.cancelled":
		var canceled struct {
			InboxID string `json:"inboxID"`
		}
		if json.Unmarshal(event.Data, &canceled) == nil {
			if index, exists := application.messageIndex[canceled.InboxID]; exists {
				application.messages[index].Pending = false
				application.messages[index].Failed = true
				application.messages[index].Acknowledged = true
				application.messages[index].Uncertain = false
			}
		}
	case "session.model.selected":
		var selected struct {
			Model modelRef `json:"model"`
		}
		if json.Unmarshal(event.Data, &selected) == nil && selected.Model.ID != "" {
			application.session.Model = &selected.Model
			application.detailsRefresh = time.Now()
		}
	case "session.step.started":
		var step struct {
			Model *modelRef `json:"model"`
		}
		if json.Unmarshal(event.Data, &step) == nil && step.Model != nil {
			application.details.lastModel = step.Model
		}
		application.busy = true
	case "session.step.ended", "session.step.failed":
		var step struct {
			Tokens *tokenUsage `json:"tokens"`
			Files  []string    `json:"files"`
		}
		if json.Unmarshal(event.Data, &step) == nil {
			if len(step.Files) > 0 && data.AssistantMessageID != "" {
				application.ensureAssistant(data.AssistantMessageID).ChangedFiles = step.Files
			}
			if step.Tokens != nil {
				model := application.effectiveModel()
				if application.details.lastModel != nil {
					model = *application.details.lastModel
				}
				application.usage = &contextUsage{tokens: *step.Tokens, model: model}
			}
		}
	case "session.tool.input.started", "session.tool.input.delta", "session.tool.input.ended", "session.tool.called", "session.tool.progress", "session.tool.success", "session.tool.failed":
		application.applyToolEvent(event)
	case "session.compaction.ended", "session.revert.staged", "session.revert.cleared", "session.revert.committed":
		application.requestSync()
	case "session.status":
		var state struct {
			Status struct{ Type, Message string } `json:"status"`
		}
		if json.Unmarshal(event.Data, &state) == nil {
			application.busy = state.Status.Type != "idle"
			if state.Status.Type == "retry" {
				application.status = "Server retrying: " + state.Status.Message
			}
			if !application.busy {
				application.requestSync()
			}
		}
	case "session.text.started":
		message := application.ensureAssistant(data.AssistantMessageID)
		message.addTextPart(data.Ordinal)
		if message.Parts == nil {
			message.Parts = make(map[int]string)
		}
		application.busy = true
	case "session.text.delta":
		message := application.ensureAssistant(data.AssistantMessageID)
		message.addTextPart(data.Ordinal)
		if message.Parts == nil {
			message.Parts = make(map[int]string)
		}
		message.Parts[data.Ordinal] += data.Delta
		application.rebuildAssistant(message)
	case "session.text.ended":
		message := application.ensureAssistant(data.AssistantMessageID)
		message.addTextPart(data.Ordinal)
		if message.Parts == nil {
			message.Parts = make(map[int]string)
		}
		message.Parts[data.Ordinal] = data.Text
		application.rebuildAssistant(message)
	case "session.execution.started":
		application.busy = true
		application.status = "OpenCode is working…"
	case "session.execution.succeeded":
		application.busy = false
		application.status = "Done"
		application.queuePreview()
		application.requestSync()
	case "session.execution.failed":
		application.busy = false
		application.status = "Execution failed"
		application.queuePreview()
		application.requestSync()
	case "session.execution.interrupted":
		application.busy = false
		application.status = "Interrupted"
		application.queuePreview()
		application.requestSync()
	case "permission.asked":
		var request permissionRequest
		if json.Unmarshal(event.Data, &request) == nil && request.SessionID == application.session.ID {
			found := false
			for _, existing := range application.permissions {
				found = found || existing.ID == request.ID
			}
			if !found {
				application.permissions = append(application.permissions, request)
			}
		}
	case "permission.replied":
		var reply struct {
			SessionID string `json:"sessionID"`
			RequestID string `json:"requestID"`
		}
		if json.Unmarshal(event.Data, &reply) == nil && reply.SessionID == application.session.ID {
			application.removePermission(reply.RequestID)
		}
	case "session.renamed":
		var renamed struct {
			SessionID string `json:"sessionID"`
			Title     string `json:"title"`
		}
		if json.Unmarshal(event.Data, &renamed) == nil && renamed.SessionID == application.session.ID {
			application.session.Title = renamed.Title
		}
	}
	application.queuePreview()
	application.dirty = true
}

func (application *app) removePermission(requestID string) {
	for index := range application.permissions {
		if application.permissions[index].ID == requestID {
			application.permissions = append(application.permissions[:index], application.permissions[index+1:]...)
			break
		}
	}
}

func (application *app) applyOperation(result operationResult) {
	if result.export != nil {
		application.applyExport(result)
		return
	}
	if result.err == nil && result.active != nil && result.activityRevision == application.activityRevision {
		application.replaceSessionActivity(result.active)
	}
	if result.kind == "delete" {
		application.applyDelete(result)
		return
	}
	if result.catalog != nil {
		application.applyCatalog(result)
		return
	}
	if result.sessionID != "" && (result.sessionID != application.session.ID || result.epoch != application.sessionEpoch) {
		return
	}
	if result.picker != nil {
		application.applyPickerResult(result)
		return
	}
	if result.file != nil {
		application.applyFileResult(result)
		return
	}
	if result.details != nil {
		application.detailsLoading = false
		if result.err == nil {
			result.details.lastModel = application.details.lastModel
			application.details = *result.details
		}
		application.dirty = true
		return
	}
	switch result.kind {
	case "attach":
		application.applyAttach(result)
		return
	case "sync":
		application.syncing = false
		if result.err == nil {
			if result.revision != application.revision {
				application.nextSync = time.Now().Add(time.Second)
				return
			}
			if result.session != nil {
				application.session = *result.session
				application.busy = result.busy
			}
			uncertain := false
			for _, message := range application.messages {
				uncertain = uncertain || message.Uncertain
			}
			application.replaceMessages(result.messages)
			application.mergeInbox(result.inbox)
			if uncertain {
				stillUncertain := false
				for _, message := range application.messages {
					stillUncertain = stillUncertain || message.Uncertain
				}
				if !stillUncertain {
					application.status = "Prompt confirmed by server"
				}
			}
			application.historyCursor = result.messageCursor
			application.permissions = result.permissions
			application.queuePreview()
			if len(application.promptIndexes()) < application.promptLimit && application.historyCursor != "" {
				application.requestSync()
			}
		}
		application.nextSync = time.Now().Add(application.syncInterval())
	case "prompt":
		application.applyPromptResult(result)
		return
	case "interrupt":
		if result.err == nil {
			application.status = "Interrupt requested"
		}
	case "permission":
		application.permissionBusy = false
		if result.err == nil {
			application.revision++
			application.removePermission(result.requestID)
			application.status = "Permission response sent"
		}
	}
	if result.err != nil {
		application.status = result.err.Error()
	}
	application.queuePreview()
	application.dirty = true
}

func (application *app) async(kind string, call func(context.Context) error) {
	id, epoch := application.session.ID, application.sessionEpoch
	go func() {
		ctx, cancel := context.WithTimeout(application.ctx, 30*time.Second)
		defer cancel()
		err := call(ctx)
		select {
		case application.operations <- operationResult{kind: kind, sessionID: id, epoch: epoch, err: err}:
		case <-application.ctx.Done():
		}
	}()
}

func (application *app) submitPrompt() {
	runes := []rune(application.input.Text())
	text := strings.TrimSpace(string(runes[application.input.Min:]))
	if text == "" {
		return
	}
	if application.session.ID == "" && !strings.HasPrefix(text, "/") {
		application.status = "Create or select a session first"
		application.dirty = true
		return
	}
	application.history = append(application.history, promptPrefix+text)
	if len(application.history) > 100 {
		application.history = application.history[len(application.history)-100:]
	}
	application.input.Reset(promptPrefix, len([]rune(promptPrefix)))
	application.input.SetHistory(application.history)
	if application.slashCommand(text) {
		application.dirty = true
		return
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		application.status = err.Error()
		application.dirty = true
		return
	}
	// V2 accepts a client-supplied message ID. Keep it stable across the
	// optimistic prompt, inbox events, and subsequent message snapshots.
	id := fmt.Sprintf("msg_%x_%x", time.Now().UnixMilli(), random)
	application.messages = append(application.messages, chatMessage{ID: id, Role: "user", Text: text, Rich: text, Pending: true})
	application.rebuildIndex()
	wasBusy := application.busy
	if !wasBusy {
		application.activePrompt = id
	}
	application.selectedPrompt, application.followPreview = id, true
	application.resetFiles()
	application.busy = true
	application.sessionActivity[application.session.ID] = true
	application.activityRevision++
	application.sidebarFocus = application.session.ID
	application.status = "Sending…"
	application.queuePreview()
	sessionID, epoch := application.session.ID, application.sessionEpoch
	application.revision++
	application.nextSync = time.Now().Add(5 * time.Second)
	previous := application.sending[sessionID]
	done := make(chan struct{})
	application.sending[sessionID] = done
	go func() {
		defer close(done)
		if previous != nil {
			select {
			case <-previous:
			case <-application.ctx.Done():
				return
			}
		}
		err := application.api.prompt(application.ctx, sessionID, text, id)
		select {
		case application.operations <- operationResult{kind: "prompt", sessionID: sessionID, epoch: epoch, requestID: id, wasBusy: wasBusy, err: err}:
		case <-application.ctx.Done():
		}
	}()
	application.dirty = true
}

func (application *app) replyPermission(decision string) {
	if len(application.permissions) == 0 || application.permissionBusy {
		return
	}
	request := application.permissions[0]
	sessionID, epoch := application.session.ID, application.sessionEpoch
	application.permissionBusy = true
	go func() {
		ctx, cancel := context.WithTimeout(application.ctx, 30*time.Second)
		defer cancel()
		err := application.api.replyPermission(ctx, sessionID, request.ID, decision)
		select {
		case application.operations <- operationResult{kind: "permission", sessionID: sessionID, epoch: epoch, requestID: request.ID, err: err}:
		case <-application.ctx.Done():
		}
	}()
}

func (application *app) interrupt() {
	id := application.session.ID
	application.async("interrupt", func(ctx context.Context) error { return application.api.interrupt(ctx, id) })
}

func (application *app) handleInput(event inputEvent) {
	if event.err != "" {
		application.status = event.err
		application.dirty = true
		return
	}
	if event.region != nil {
		application.sidebarHits, application.deleteHits, application.exportHits = nil, nil, nil
		application.rows, application.cols = event.region.mainRows, event.region.mainColumns
		if err := application.preview.setReady(event.region); err != nil {
			application.status = err.Error()
		}
		application.dirty = true
		return
	}
	if application.deleteConfirmation != nil {
		application.handleDeleteInput(event)
		return
	}
	if application.exportDialog != nil {
		application.handleExportInput(event)
		return
	}
	if event.mouse != nil {
		application.handleMouse(*event.mouse)
		return
	}
	if application.attachTarget != "" {
		switch event.key {
		case "\x11":
			application.running = false
		case "\x03", "<Esc>":
			application.cancelAttach()
			application.queuePreview()
		}
		return
	}
	if len(application.permissions) > 0 {
		switch event.key {
		case "y", "Y":
			application.replyPermission("once")
		case "a", "A":
			application.replyPermission("always")
		case "n", "N", "<Esc>":
			application.replyPermission("reject")
		case "\x11":
			application.running = false
		case "\x03":
			if application.busy {
				application.interrupt()
			}
		case "<PageUp>":
			application.scroll = max(0, application.scroll-max(1, application.rows/2))
		case "<PageDown>":
			application.scroll += max(1, application.rows/2)
		case "\x15": // Ctrl-U
			application.scrollResponseBy(-1, true)
		case "\x04": // Ctrl-D
			application.scrollResponseBy(1, true)
		}
		application.dirty = true
		return
	}
	if application.picker != nil {
		application.handlePickerInput(event)
		return
	}
	if event.paste != "" {
		if err := application.input.Insert(event.paste); err != nil {
			application.status = err.Error()
		}
		application.dirty = true
		return
	}
	switch event.key {
	case "<Tab>":
		application.toggleFiles()
		return
	case "\x11": // Ctrl-Q
		application.running = false
		return
	case "\x03": // Ctrl-C
		if application.busy {
			application.interrupt()
		} else {
			application.running = false
		}
		return
	case "<PageUp>":
		application.scroll = max(0, application.scroll-max(1, application.rows/2))
		application.dirty = true
		return
	case "<PageDown>":
		application.scroll += max(1, application.rows/2)
		application.dirty = true
		return
	case "<A-Up>":
		if application.filesMode {
			application.moveFile(-1)
			return
		}
		application.movePreview(-1)
		return
	case "<A-Down>":
		if application.filesMode {
			application.moveFile(1)
			return
		}
		application.movePreview(1)
		return
	case "<A-PageUp>":
		application.scrollResponse(-1)
		return
	case "<A-PageDown>":
		application.scrollResponse(1)
		return
	case "\x15": // Ctrl-U
		application.scrollResponseBy(-1, true)
		return
	case "\x04": // Ctrl-D
		application.scrollResponseBy(1, true)
		return
	case "<F3>":
		if application.filesMode {
			application.reloadFile()
			return
		}
		application.morePrompts()
		return
	case "<F2>":
		if !application.preview.visible && !application.preview.canStart(application.cols, application.rows) {
			application.status = "Terminal is too small for the rich pane"
		} else if err := application.preview.toggle(time.Now()); err != nil {
			application.status = err.Error()
		} else if application.preview.visible {
			application.status = "Rich preview enabled"
		} else {
			application.status = "Rich preview hidden"
		}
		application.dirty = true
		return
	}
	result := application.input.Handle(event.key)
	if result.Err != nil {
		application.status = result.Err.Error()
	}
	if result.Submit {
		application.submitPrompt()
	} else if result.Cancel {
		if len([]rune(application.input.Text())) > application.input.Min {
			application.input.Reset(promptPrefix, len([]rune(promptPrefix)))
			application.input.SetHistory(application.history)
		}
	}
	if result.Handled || result.DisplayChanged {
		application.dirty = true
	}
}

func makeRaw(fd int) (*unix.Termios, error) {
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, fmt.Errorf("stdin must be an interactive terminal: %w", err)
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 0, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return nil, err
	}
	return old, nil
}

func (application *app) terminalSize() {
	application.sidebarHits, application.deleteHits, application.exportHits = nil, nil, nil
	if size, err := unix.IoctlGetWinsize(application.fd, unix.TIOCGWINSZ); err == nil {
		application.rows = max(5, min(500, int(size.Row)))
		application.cols = max(8, min(1000, int(size.Col)))
	}
	application.dirty = true
}

func (application *app) run() error {
	old, err := makeRaw(application.fd)
	if err != nil {
		return err
	}
	defer unix.IoctlSetTermios(application.fd, unix.TCSETS, old)
	defer application.cancel()
	defer application.input.Close()
	defer application.closePicker()
	defer application.closeDeleteConfirmation()
	defer application.closeExportDialog()
	defer application.cancelAttach()
	defer application.cancelFile()
	application.terminalSize()
	signal.Notify(application.signals, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(application.signals)
	fmt.Fprint(application.out, "\x1b[?1049h\x1b[?2004h\x1b[?1000h\x1b[?1006h\x1b[?25h")
	defer fmt.Fprint(application.out, "\x18\x1b[?2026l\x1b[?1006l\x1b[?1000l\x1b[?2004l\x1b[0 q\x1b[0m\x1b[?25h\x1b[?1049l")
	if application.preview.canStart(application.cols, application.rows) {
		if err := application.preview.start(time.Now()); err != nil {
			return err
		}
	} else if application.preview.enabled {
		application.status = "Terminal is too small for the rich pane; press F2 after resizing"
	}
	defer application.preview.close()
	application.queuePreview()
	application.startEventStream()
	application.requestCatalog()
	application.detailsRefresh = time.Now()
	application.nextSync = time.Now().Add(5 * time.Second)

	var parser inputParser
	var input [8192]byte
	for application.running {
	drain:
		for count := 0; count < 256; count++ {
			select {
			case item := <-application.stream:
				application.applyStream(item)
			case result := <-application.operations:
				application.applyOperation(result)
			case received := <-application.signals:
				switch received {
				case syscall.SIGWINCH:
					application.terminalSize()
				case syscall.SIGINT:
					application.handleInput(inputEvent{key: "\x03"})
				default:
					application.running = false
				}
			default:
				break drain
			}
		}
		now := time.Now()
		application.tickPicker(now)
		application.tickConnection(now)
		if !application.catalogRefresh.IsZero() && !now.Before(application.catalogRefresh) && !application.catalogLoading {
			application.catalogRefresh = time.Time{}
			application.requestCatalog()
		}
		parser.expire(now, application.handleInput)
		if result := application.activeInput().Poll(); result.DisplayChanged || result.Err != nil {
			if result.Err != nil {
				application.status = result.Err.Error()
			}
			application.dirty = true
		}
		if status, err := application.preview.tick(now); err != nil {
			return err
		} else if status != "" {
			application.status = status
			application.dirty = true
		}
		if application.dirty && (application.lastDraw.IsZero() || now.Sub(application.lastDraw) >= 20*time.Millisecond) {
			if err := application.draw(); err != nil {
				return err
			}
			application.lastDraw = now
		}
		poll := []unix.PollFd{{Fd: int32(application.fd), Events: unix.POLLIN}}
		_, err := unix.Poll(poll, 20)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if poll[0].Revents&unix.POLLIN != 0 {
			count, readErr := unix.Read(application.fd, input[:])
			if count > 0 {
				parser.feed(input[:count], time.Now(), application.handleInput)
			}
			if readErr != nil && readErr != unix.EINTR && readErr != unix.EAGAIN {
				return readErr
			}
		}
		if poll[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			break
		}
	}
	return nil
}
