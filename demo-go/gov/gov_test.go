package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"st-go/platform_neutral/api"
	"st-go/tui/video"
)

func TestFragmentedStatusAndKeyboardControls(t *testing.T) {
	var out bytes.Buffer
	client, err := video.New(&out, "secret")
	if err != nil {
		t.Fatal(err)
	}
	v := &viewer{out: &out, client: client, cols: 80, rows: 24, options: options{file: "/tmp/';\nmovie.mp4", volume: 80}}
	var parser inputParser
	data, _ := json.Marshal(api.VideoStatus{Supported: true, State: "idle"})
	reply := "\x1bPvideo " + client.ID + " " + base64.RawStdEncoding.EncodeToString(data) + "\x1b\\"
	for _, b := range []byte(reply) {
		parser.feed([]byte{b}, time.Now(), v.key, func(s string) { v.reply(s) })
	}
	if !v.ready || v.err != nil {
		t.Fatal("player did not open", v.err)
	}
	fields := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(out.String(), "\x1bP"), "\x1b\\"))
	raw, err := base64.RawStdEncoding.DecodeString(fields[4])
	if err != nil {
		t.Fatal(err)
	}
	var opened api.VideoRequest
	if err := json.Unmarshal(raw, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.File != v.options.file || opened.Bounds.Height != 22 || opened.Volume != 80 {
		t.Fatal(opened)
	}
	out.Reset()
	v.status = api.VideoStatus{Supported: true, State: "playing", PositionMS: 6000, Volume: 80}
	parser.feed([]byte("\x1b["), time.Now(), v.key, func(string) {})
	parser.feed([]byte("D m+q"), time.Now(), v.key, func(string) {})
	if !v.quit || strings.Count(out.String(), "video auth") != 4 {
		t.Fatalf("controls failed: %q", out.String())
	}
	v.lastTop = ""
	out.Reset()
	v.draw()
	if strings.Contains(out.String(), "\nmovie") {
		t.Fatal("filename injected terminal control characters")
	}
}
func TestStaleRepliesAndBoundedIncompleteInput(t *testing.T) {
	client, _ := video.New(io.Discard, "secret")
	if _, ok := client.Reply("video 00112233445566778899aabbccddeeff e30"); ok {
		t.Fatal("stale reply accepted")
	}
	var p inputParser
	p.feed([]byte("\x1bP"+strings.Repeat("x", 40000)), time.Now(), func(string) {}, func(string) {})
	if len(p.pending) != 0 {
		t.Fatal("unbounded pending reply")
	}
	keys := 0
	now := time.Now()
	p.feed([]byte("\x1b"), now, func(string) {}, func(string) {})
	p.expire(now.Add(time.Second), func(s string) {
		if s == "escape" {
			keys++
		}
	})
	if keys != 1 {
		t.Fatal("escape timeout failed")
	}
}
