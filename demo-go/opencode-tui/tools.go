package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type responsePart struct {
	ordinal int
	toolID  string
}

func (message *chatMessage) addTextPart(ordinal int) {
	for _, part := range message.Order {
		if part.toolID == "" && part.ordinal == ordinal {
			return
		}
	}
	message.Order = append(message.Order, responsePart{ordinal: ordinal})
}

func rebuildMessage(message *chatMessage) {
	// Older/local messages can contain text parts without an ordered timeline.
	var missing []int
	for ordinal := range message.Parts {
		found := false
		for _, part := range message.Order {
			found = found || part.toolID == "" && part.ordinal == ordinal
		}
		if !found {
			missing = append(missing, ordinal)
		}
	}
	sort.Ints(missing)
	for _, ordinal := range missing {
		message.addTextPart(ordinal)
	}
	var text, rich, auxiliary []string
	for _, part := range message.Order {
		value := message.Parts[part.ordinal]
		if part.toolID != "" {
			value = ""
			for _, tool := range message.Tools {
				if tool.ID == part.toolID {
					value = toolSummary(tool)
					break
				}
			}
			if value != "" {
				auxiliary = append(auxiliary, value)
			}
		} else if value != "" {
			rich = append(rich, value)
		}
		if value != "" {
			text = append(text, value)
		}
	}
	message.Text = strings.Join(text, "\n\n")
	message.Rich = strings.Join(rich, "\n\n")
	message.Aux = strings.Join(auxiliary, "\n\n")
}

// Fence arbitrary command/source text without interpreting embedded Markdown.
func codeBlock(text, language string) string {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	return fence + language + "\n" + text + "\n" + fence
}

func toolInput(tool messageContent) map[string]any {
	var input map[string]any
	if json.Unmarshal(tool.State.Input, &input) == nil {
		return input
	}
	// Streaming input is a JSON string containing the arguments accumulated so far.
	var raw string
	if json.Unmarshal(tool.State.Input, &raw) == nil {
		_ = json.Unmarshal([]byte(raw), &input)
	}
	return input
}

func toolSummary(tool messageContent) string {
	status := tool.State.Status
	if status == "" {
		status = "pending"
	}
	name := tool.Name
	if name == "" {
		name = "tool"
	}
	result := fmt.Sprintf("[tool: %s — %s]", safeText(name), safeText(status))
	input := toolInput(tool)
	for _, key := range []string{"command", "code"} {
		if command, ok := input[key].(string); ok && command != "" {
			result += "\n\n" + codeBlock(safeText(command), "")
			break
		}
	}
	files := toolFiles(tool)
	if len(files) > 0 {
		var names []string
		for _, file := range files {
			names = append(names, safeText(file.Name))
		}
		result += "\n\nFiles:\n\n" + codeBlock(strings.Join(names, "\n"), "")
	}
	if tool.State.Error != nil && tool.State.Error.Message != "" {
		result += "\n\n" + codeBlock(safeText(tool.State.Error.Message), "")
	}
	return result
}

func (application *app) applyToolEvent(event apiEvent) {
	var data struct {
		SessionID, AssistantMessageID, ID, Name, Delta, Text string
		Input                                                json.RawMessage
		Content                                              []toolContent
		Metadata                                             map[string]any
		Error                                                *struct {
			Message string `json:"message"`
		}
	}
	if json.Unmarshal(event.Data, &data) != nil || data.ID == "" || data.AssistantMessageID == "" {
		return
	}
	message := application.ensureAssistant(data.AssistantMessageID)
	index := -1
	for i := range message.Tools {
		if message.Tools[i].ID == data.ID {
			index = i
			break
		}
	}
	if index == -1 {
		message.Tools = append(message.Tools, messageContent{Type: "tool", ID: data.ID, Name: data.Name})
		index = len(message.Tools) - 1
		message.Order = append(message.Order, responsePart{toolID: data.ID})
	}
	tool := &message.Tools[index]
	if data.Name != "" {
		tool.Name = data.Name
	}
	switch event.Type {
	case "session.tool.input.started":
		if tool.State.Status == "" {
			tool.State.Status = "streaming"
		}
	case "session.tool.input.delta":
		if tool.State.Status != "" && tool.State.Status != "streaming" {
			return
		}
		var previous string
		_ = json.Unmarshal(tool.State.Input, &previous)
		if len(previous)+len(data.Delta) > maxAPIResponse {
			return
		}
		tool.State.Input, _ = json.Marshal(previous + data.Delta)
		tool.State.Status = "streaming"
	case "session.tool.input.ended":
		if tool.State.Status != "" && tool.State.Status != "streaming" {
			return
		}
		tool.State.Input, _ = json.Marshal(data.Text)
		tool.State.Status = "streaming"
	case "session.tool.called":
		tool.State.Input = data.Input
		if tool.State.Status != "completed" && tool.State.Status != "error" {
			tool.State.Status = "running"
		}
	case "session.tool.success":
		tool.State.Status, tool.State.Content = "completed", data.Content
	case "session.tool.failed":
		tool.State.Status, tool.State.Content, tool.State.Error = "error", data.Content, data.Error
	}
	if data.Metadata != nil {
		if tool.State.Metadata == nil {
			tool.State.Metadata = make(map[string]any)
		}
		for key, value := range data.Metadata {
			tool.State.Metadata[key] = value
		}
	}
	rebuildMessage(message)
}
