package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func answer(id, text string) apiMessage {
	return apiMessage{ID: id, Type: "assistant", Content: []messageContent{{Type: "text", Text: text}}}
}

func promptTestApp(t *testing.T, messages []apiMessage, rich bool) (*app, *bytes.Buffer) {
	t.Helper()
	var output bytes.Buffer
	view := &richPreview{out: &output, width: 58, enabled: rich, visible: rich, ready: rich}
	application := newApp(nil, sessionInfo{ID: "ses_test"}, messages, nil, &output, -1, view)
	t.Cleanup(func() { application.cancel(); application.input.Close() })
	return application, &output
}

func TestPromptClickGroupsResponsesAndPinsSelection(t *testing.T) {
	messages := []apiMessage{
		{ID: "msg_one", Type: "user", Text: "Explain this long prompt with 世界 and wrapped lines"},
		answer("msg_a", "First response"), answer("msg_b", "Final result $x^2$"),
		{ID: "msg_two", Type: "user", Text: "Another question"}, answer("msg_c", "Second response"),
	}
	application, _ := promptTestApp(t, messages, true)
	application.cols = 28
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	var wrapped []int
	for y := 1; y <= application.rows; y++ {
		if hit := application.sidebarHits[y]; hit.kind == "prompt" && hit.id == "msg_one" {
			wrapped = append(wrapped, y)
		}
	}
	if len(wrapped) < 2 {
		t.Fatal("fixture did not wrap")
	}
	for _, row := range application.promptRows(application.cols) {
		if strings.Contains(row.text, "response") || strings.Contains(row.text, "Final result") {
			t.Fatal("assistant text leaked into the prompt list")
		}
	}
	application.handleInput(inputEvent{mouse: &mouseEvent{x: 8, y: wrapped[1], release: true}})
	if application.selectedPrompt != "msg_two" {
		t.Fatal("release selected a prompt")
	}
	application.handleInput(inputEvent{mouse: &mouseEvent{x: 8, y: wrapped[1]}})
	if application.selectedPrompt != "msg_one" || application.followPreview {
		t.Fatal("wrapped-line click did not pin the prompt")
	}
	response := application.selectedResponse()
	if !strings.Contains(response, "First response") || !strings.Contains(response, "Final result") || strings.Contains(response, "Second response") {
		t.Fatalf("wrong grouped response: %q", response)
	}
	if application.preview.pending == nil || application.preview.pending.text != response {
		t.Fatal("rich pane was not queued with the selected response")
	}
	application.applyEvent(apiEvent{Type: "session.text.delta", Data: json.RawMessage(`{"sessionID":"ses_test","assistantMessageID":"msg_c","ordinal":0,"delta":" continuing"}`)})
	if application.selectedPrompt != "msg_one" || application.selectedResponse() != response {
		t.Fatal("streaming stole selection")
	}
	messages[4] = answer("msg_c", "Second response continuing")
	application.replaceMessages(messages)
	if application.selectedPrompt != "msg_one" {
		t.Fatal("resync lost prompt selection")
	}
	application.handleInput(inputEvent{key: "<A-Up>"})
	if application.selectedPrompt != "msg_two" || application.preview.pending.text != "Second response continuing" {
		t.Fatal("keyboard selection did not update response")
	}
}

func TestPromptMouseHitMapScrollResizeAndTextPane(t *testing.T) {
	var messages []apiMessage
	for n := 0; n < 12; n++ {
		messages = append(messages, apiMessage{ID: fmt.Sprintf("msg_p%d", n), Type: "user", Text: fmt.Sprintf("Question %d", n)}, answer(fmt.Sprintf("msg_a%d", n), strings.Repeat(fmt.Sprintf("Answer %d\n", n), 30)))
	}
	application, _ := promptTestApp(t, messages, false)
	application.rows, application.cols = 12, 60
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	left := application.paintedLayout.prompts
	initialScroll := application.scroll
	application.handleInput(inputEvent{mouse: &mouseEvent{button: 65, x: 2, y: left.y}})
	if application.scroll != initialScroll+3 {
		t.Fatal("wheel did not scroll prompt list")
	}
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	y, id := 0, ""
	for row := left.y; row < left.y+left.height; row++ {
		if hit := application.sidebarHits[row]; hit.kind == "prompt" {
			y, id = row, hit.id
			break
		}
	}
	application.handleInput(inputEvent{mouse: &mouseEvent{x: 2, y: y}})
	if application.selectedPrompt != id {
		t.Fatal("scrolled row selected the wrong prompt")
	}
	right := application.paintedLayout.response
	before := application.selectedPrompt
	application.handleInput(inputEvent{mouse: &mouseEvent{x: right.x, y: y}})
	application.handleInput(inputEvent{mouse: &mouseEvent{button: 65, x: right.x, y: right.y}})
	if application.selectedPrompt != before || application.scroll != initialScroll+3 || application.responseScroll == 0 {
		t.Fatal("response pane mouse traffic changed prompt selection/scroll")
	}
	application.cols = 40 // Simulate SIGWINCH before the next draw.
	application.handleInput(inputEvent{mouse: &mouseEvent{x: 2, y: y + 2}})
	if application.selectedPrompt != before {
		t.Fatal("stale geometry accepted a click")
	}
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	application.handleInput(inputEvent{mouse: &mouseEvent{x: 2, y: application.rows - 1}})
	if application.selectedPrompt != before {
		t.Fatal("composer click selected a prompt")
	}
	application.rows, application.cols = 5, 8
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	if layout := application.paintedLayout; layout.response.x+layout.response.width-1 > 8 || layout.prompts.height != 1 {
		t.Fatalf("invalid compact panes: %+v", layout)
	}
}

func TestQueuedPromptKeepsCurrentResponseAssociation(t *testing.T) {
	ids := make(chan string, 1)
	application := pickerTestApp(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/session/ses_test/prompt" {
			t.Errorf("unexpected request: %s", request.URL)
		}
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		ids <- body["id"]
		io.WriteString(writer, `{"data":{}}`)
	}, sessionInfo{ID: "ses_test"})
	application.replaceMessages([]apiMessage{{ID: "msg_old", Type: "user", Text: "Original"}, answer("msg_a", "Original answer")})
	application.busy = true
	enterCommand(t, application, "Queued question")
	awaitOperation(t, application, "prompt")
	id := <-ids
	if id != application.selectedPrompt || !strings.HasPrefix(id, "msg_") {
		t.Fatal("optimistic prompt ID differs from the server request")
	}
	application.applyEvent(apiEvent{Type: "session.text.started", Data: json.RawMessage(`{"sessionID":"ses_test","assistantMessageID":"msg_cont","ordinal":0}`)})
	application.applyEvent(apiEvent{Type: "session.text.delta", Data: json.RawMessage(`{"sessionID":"ses_test","assistantMessageID":"msg_cont","ordinal":0,"delta":"Original continuation"}`)})
	if strings.Contains(application.selectedResponse(), "Original") {
		t.Fatal("queued prompt was assigned the preceding response")
	}
	application.applyEvent(apiEvent{Type: "session.inbox.delivered", Data: json.RawMessage(fmt.Sprintf(`{"sessionID":"ses_test","inboxID":%q}`, id))})
	application.applyEvent(apiEvent{Type: "session.text.delta", Data: json.RawMessage(`{"sessionID":"ses_test","assistantMessageID":"msg_new","ordinal":0,"delta":"Queued answer"}`)})
	if application.selectedResponse() != "Queued answer" {
		t.Fatalf("queued response = %q", application.selectedResponse())
	}
	application.replaceMessages([]apiMessage{{ID: "msg_old", Type: "user", Text: "Original"}, answer("msg_a", "Original answer"), answer("msg_cont", "Original continuation"), {ID: id, Type: "user", Text: "Queued question"}, answer("msg_new", "Queued answer")})
	if len(application.promptIndexes()) != 2 || application.selectedPrompt != id {
		t.Fatal("snapshot duplicated or lost the pending prompt")
	}
}

func TestRichSelectionInvalidatesOldRenderAndShowsPendingPlaceholder(t *testing.T) {
	application, output := promptTestApp(t, []apiMessage{{ID: "msg_one", Type: "user", Text: "One"}, answer("msg_a", "One answer"), {ID: "msg_two", Type: "user", Text: "Two"}}, true)
	application.selectPrompt("msg_one")
	old := *application.preview.pending
	application.selectPrompt("msg_two")
	view := application.preview
	if !strings.Contains(view.pending.text, "No response") || !strings.Contains(output.String(), "region secondary clear") {
		t.Fatal("pending selection retained the previous answer")
	}
	view.results = make(chan richRenderResult, 1)
	view.results <- richRenderResult{request: old, pdf: []byte("old response")}
	view.nextRender = time.Now().Add(time.Hour) // Isolate completion of the old render.
	if _, err := view.tick(time.Now()); err != nil {
		t.Fatal(err)
	}
	if view.published != 0 {
		t.Fatal("old response was published after selecting another prompt")
	}
}

func TestRepeatedPromptClickJumpsToEndAndControlKeysPagePreview(t *testing.T) {
	for _, mode := range []string{"text", "pdf", "web"} {
		t.Run(mode, func(t *testing.T) {
			application, output := promptTestApp(t, []apiMessage{
				{ID: "msg_one", Type: "user", Text: "First question"}, answer("msg_a", strings.Repeat("Long answer\n", 100)),
				{ID: "msg_two", Type: "user", Text: "Second question"}, answer("msg_b", "Another answer"),
			}, mode != "text")
			application.preview.web = mode == "web"
			application.input.Insert("unfinished draft")
			sidebarClick(t, application, "prompt", "msg_one")
			if application.preview.endPending || application.responseScroll != 0 || strings.Contains(output.String(), "position 1000000") {
				t.Fatal("first click jumped past the start")
			}
			if err := application.draw(); err != nil {
				t.Fatal(err)
			}
			view := application.preview
			view.displayed, view.published = view.generation, view.generation
			generation, fileSequence, scroll := view.generation, application.fileSequence, application.scroll
			output.Reset()
			clicked := false
			for y, hit := range application.sidebarHits {
				if hit.kind == "prompt" && hit.id == "msg_one" {
					application.handleInput(inputEvent{mouse: &mouseEvent{x: 2, y: y}})
					clicked = true
					break
				}
			}
			if !clicked {
				t.Fatal("selected prompt is not clickable")
			}
			if application.scroll != scroll || application.fileSequence != fileSequence || view.generation != generation || application.followPreview {
				t.Fatal("repeat click reselected/reloaded the prompt or moved the sidebar")
			}
			if mode == "text" {
				if application.responseScroll != application.responseEnd() || application.responseScroll == 0 {
					t.Fatal("repeat click did not reach the end")
				}
			} else if output.String() != "\x1bPregion secondary position 1000000\x1b\\" {
				t.Fatalf("repeat click navigation: %q", output.String())
			}
			output.Reset()
			end := application.responseScroll
			application.handleInput(inputEvent{key: "\x15"})
			if mode == "text" && application.responseScroll != end-application.bodyLayout().response.height {
				t.Fatal("Ctrl-U did not move a full screen")
			}
			application.handleInput(inputEvent{key: "\x04"})
			if mode == "text" {
				if application.responseScroll != end {
					t.Fatal("Ctrl-D did not move a full screen")
				}
				application.handleInput(inputEvent{key: "\x04"})
				if application.responseScroll != end {
					t.Fatal("Ctrl-D overshot the end")
				}
			} else if output.String() != "\x1bPregion secondary view -1000000\x1b\\\x1bPregion secondary view 1000000\x1b\\" {
				t.Fatalf("full-page commands: %q", output.String())
			}
			if application.input.Text() != promptPrefix+"unfinished draft" || !application.running || application.selectedPrompt != "msg_one" {
				t.Fatal("paging edited the draft or changed selection")
			}
			application.filesMode = true
			application.fileSelection = &fileSelection{text: strings.Repeat("File line\n", 100)}
			application.responseScroll = 0
			output.Reset()
			application.handleInput(inputEvent{key: "\x04"})
			if mode == "text" && application.responseScroll != application.bodyLayout().response.height || mode != "text" && !strings.Contains(output.String(), "view 1000000") {
				t.Fatal("file preview paging failed")
			}
		})
	}
}

func TestLongPromptFoldingPreservesResponseAndDraft(t *testing.T) {
	for _, rich := range []bool{false, true} {
		t.Run(fmt.Sprintf("rich=%v", rich), func(t *testing.T) {
			text := strings.Repeat("Long document paragraph with 世界.\n", 100) + "Last source line"
			application, _ := promptTestApp(t, []apiMessage{
				{ID: "msg_long", Type: "user", Text: text}, answer("answer_long", "Earlier answer"),
				{ID: "msg_short", Type: "user", Text: "Short question"}, answer("answer_short", "Selected answer"),
			}, rich)
			application.input.Insert("unfinished draft")
			countLines := func() int {
				count := 0
				for _, row := range application.promptRows(application.bodyLayout().prompts.width - 2) {
					if row.promptID == "msg_long" && row.action == "" {
						count++
					}
				}
				return count
			}
			if countLines() != 3 {
				t.Fatal("long prompt did not start collapsed to three lines")
			}
			generation, follow := application.preview.generation, application.followPreview
			sidebarClick(t, application, "fold", "msg_long")
			if countLines() <= 100 || application.selectedPrompt != "msg_short" || application.selectedResponse() != "Selected answer" {
				t.Fatal("expanding an unselected prompt lost text or changed the response")
			}
			// Collapse using the bottom control after scrolling through the text.
			for index, row := range application.sidebarRows(application.bodyLayout().prompts.width) {
				if row.promptID == "msg_long" && row.action == "fold" {
					application.scroll = index
				}
			}
			if err := application.draw(); err != nil {
				t.Fatal(err)
			}
			clicked := false
			for y, hit := range application.sidebarHits {
				if hit.kind == "fold" && hit.id == "msg_long" {
					var parser inputParser
					parser.feed([]byte(fmt.Sprintf("\x1b[<0;3;%dM", y)), time.Now(), application.handleInput)
					clicked = true
					break
				}
			}
			if !clicked || countLines() != 3 {
				t.Fatal("bottom fold control did not collapse prompt")
			}
			if application.followPreview != follow || application.preview.generation != generation || application.responseScroll != 0 || application.preview.endPending || application.input.Text() != promptPrefix+"unfinished draft" {
				t.Fatal("folding changed preview state or composer draft")
			}
			if application.messages[application.messageIndex["msg_long"]].Text != text {
				t.Fatal("folding changed saved prompt text")
			}
			if err := application.draw(); err != nil {
				t.Fatal(err)
			}
			visible := false
			for _, hit := range application.sidebarHits {
				visible = visible || hit.kind == "prompt" && hit.id == "msg_long"
			}
			if !visible {
				t.Fatal("folding from the bottom lost the prompt offscreen")
			}
		})
	}
}

func TestPromptFoldKeyboardRefreshResizeAndSessionIsolation(t *testing.T) {
	messages := []apiMessage{{ID: "msg_long", Type: "user", Text: strings.Repeat("中文 é paragraph\n", 20)}, answer("answer", "Response")}
	application, _ := promptTestApp(t, messages, true)
	key := promptFoldKey{"ses_test", "msg_long"}
	var parser inputParser
	for _, b := range []byte("\x1bOS") {
		parser.feed([]byte{b}, time.Now(), application.handleInput)
	}
	if !application.expandedPrompts[key] {
		t.Fatal("F4 did not expand selected prompt")
	}
	application.replaceMessages(messages)
	application.applyEvent(apiEvent{Type: "session.text.delta", Data: json.RawMessage(`{"sessionID":"ses_test","assistantMessageID":"answer","ordinal":0,"delta":" continued"}`)})
	for _, width := range []int{8, 28, 100} {
		application.handleInput(inputEvent{region: &regionLayout{mainColumns: width, mainRows: 24}})
		if err := application.draw(); err != nil {
			t.Fatal(err)
		}
		if !application.expandedPrompts[key] {
			t.Fatal("refresh/stream/resize lost expansion")
		}
		for _, row := range application.promptRows(width) {
			if row.action == "" && textWidth(row.text) > width {
				t.Fatalf("wide-character prompt exceeded width %d: %q", width, row.text)
			}
		}
	}
	application.session.ID = "ses_other"
	for _, row := range application.promptRows(80) {
		if row.action == "fold" && !strings.Contains(row.text, "Show more") {
			t.Fatal("another session inherited a prompt's expanded state")
		}
	}
	application.session.ID = "ses_test"
	if !application.expandedPrompts[key] {
		t.Fatal("switching sessions lost expansion")
	}
	parser.feed([]byte("\x1b[14~"), time.Now(), application.handleInput)
	if application.expandedPrompts[key] || application.selectedPrompt != "msg_long" {
		t.Fatal("alternate F4 encoding failed to fold selected prompt")
	}
}
