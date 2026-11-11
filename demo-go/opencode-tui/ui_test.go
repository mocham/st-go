package main

import (
	"strings"
	"testing"
)

func TestSafeTextAndCellWrapping(t *testing.T) {
	got := safeText("ok\x1b[31m\x00\n世界")
	if strings.ContainsAny(got, "\x1b\x00") || !strings.Contains(got, "\n世界") {
		t.Fatalf("safe text = %q", got)
	}
	rows := wrapText("ab世界c", 4)
	if len(rows) != 2 || textWidth(rows[0]) > 4 || textWidth(rows[1]) > 4 {
		t.Fatalf("wrapped rows = %#v", rows)
	}
}

func TestConvertMessagesKeepsRichTextAndToolStatus(t *testing.T) {
	message := apiMessage{ID: "assistant", Type: "assistant"}
	text := messageContent{Type: "text", Text: "Use $x^2$."}
	tool := messageContent{Type: "tool", Name: "shell"}
	tool.State.Status = "completed"
	message.Content = []messageContent{text, tool}
	converted := convertMessages([]apiMessage{message})
	if len(converted) != 1 || converted[0].Rich != "Use $x^2$." || !strings.Contains(converted[0].Text, "shell — completed") {
		t.Fatalf("converted = %+v", converted)
	}
}
