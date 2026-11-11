package main

import (
	"strings"
	"testing"
	"time"
)

func TestInputParserRegionAndPasteIsolation(t *testing.T) {
	var events []inputEvent
	parser := inputParser{}
	emit := func(event inputEvent) { events = append(events, event) }
	parser.feed([]byte("\x1bPregion layout main 1 1 42 24 secondary 43 1 58 24\x1b\\"), time.Now(), emit)
	if len(events) != 1 || events[0].region == nil || events[0].region.mainColumns != 42 || events[0].region.mainRows != 24 {
		t.Fatalf("region events = %+v", events)
	}
	events = nil
	pasted := "literal \x1bPregion layout main 1 1 10 10 secondary 11 1 10 10\x1b\\ text"
	parser.feed([]byte("\x1b[200~"+pasted+"\x1b[201~"), time.Now(), emit)
	if len(events) != 1 || events[0].paste != pasted || events[0].region != nil {
		t.Fatalf("paste events = %+v", events)
	}
}

func TestSGRMouseFragmentationAndPasteIsolation(t *testing.T) {
	var parser inputParser
	var events []inputEvent
	emit := func(event inputEvent) { events = append(events, event) }
	now := time.Now()
	for _, b := range []byte("\x1b[<0;12;7M\x1b[<0;12;7m\x1b[<65;3;4M") {
		parser.feed([]byte{b}, now, emit)
	}
	if len(events) != 3 || events[0].mouse == nil || events[0].mouse.x != 12 || events[0].mouse.y != 7 || !events[1].mouse.release || events[2].mouse.button != 65 {
		t.Fatalf("mouse events: %+v", events)
	}
	events = nil
	for _, sequence := range []string{"\x1b[<0;0;7M", "\x1b[<0;-2;7M", "\x1b[<0;1;2;3M", "\x1b[<999;1;2M", "\x1b[<0;" + strings.Repeat("9", 140) + ";1M"} {
		parser.feed([]byte(sequence), now, emit)
	}
	parser.feed([]byte("\x1b[<0;5;"), now, emit)
	parser.expire(now.Add(time.Second), emit)
	parser.feed([]byte("x"), now.Add(time.Second), emit)
	parser.feed([]byte("\x1b[200~\x1b[<0;1;2M\x1b[201~"), now.Add(time.Second), emit)
	if len(events) != 2 || events[0].key != "x" || events[1].paste != "\x1b[<0;1;2M" || events[1].mouse != nil {
		t.Fatalf("malformed/pasted reports leaked mouse input: %+v", events)
	}
}

func TestParseRegionLayoutRejectsMalformedGeometry(t *testing.T) {
	for _, value := range []string{
		"region layout main 1 1 42 24 secondary 44 1 58 24",
		"region layout main 0 1 42 24 secondary 43 1 58 24",
		"region layout main 1 1 -1 24 secondary 1 1 58 24",
		"unrelated reply",
	} {
		if layout := parseRegionLayout(value); layout != nil {
			t.Fatalf("accepted %q: %+v", value, layout)
		}
	}
}

func TestInputParserExpiresIncompleteDCS(t *testing.T) {
	start := time.Now()
	parser := inputParser{}
	var events []inputEvent
	parser.feed([]byte("\x1bPregion layout"), start, func(event inputEvent) { events = append(events, event) })
	parser.expire(start.Add(3*time.Second), func(event inputEvent) { events = append(events, event) })
	if parser.inDCS || len(events) != 1 || events[0].err == "" {
		t.Fatalf("parser = %+v, events = %+v", parser, events)
	}
	parser.feed([]byte("x"), start.Add(4*time.Second), func(event inputEvent) { events = append(events, event) })
	if len(events) != 2 || events[1].key != "x" {
		t.Fatalf("parser did not recover: %+v", events)
	}
}
