package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
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
	if opened.File != v.options.file || opened.Bounds.Height != 21 || opened.Volume != 80 {
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

func govRequests(t *testing.T, output string) []api.VideoRequest {
	t.Helper()
	var requests []api.VideoRequest
	for _, part := range strings.Split(output, "\x1bP")[1:] {
		end := strings.Index(part, "\x1b\\")
		if end < 0 {
			t.Fatal("incomplete request", part)
		}
		fields := strings.Fields(part[:end])
		if len(fields) != 5 {
			t.Fatal("incorrect request", fields)
		}
		data, err := base64.RawStdEncoding.DecodeString(fields[4])
		if err != nil {
			t.Fatal(err)
		}
		var r api.VideoRequest
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, r)
	}
	return requests
}

func TestSpeedAndVolumeKeysAccumulateAcrossStaleReplies(t *testing.T) {
	var out bytes.Buffer
	client, _ := video.New(&out, "secret")
	v := &viewer{out: &out, client: client, ready: true, status: api.VideoStatus{Supported: true, Speed: 1, Volume: 50}}
	stale, _ := json.Marshal(v.status)
	reply := "video " + client.ID + " " + base64.RawStdEncoding.EncodeToString(stale)
	v.key("]")
	v.reply(reply)
	v.key("]")
	v.key("[")
	v.key("+")
	v.reply(reply)
	v.key("+")
	v.key("-")
	requests := govRequests(t, out.String())
	if len(requests) != 6 {
		t.Fatal(requests)
	}
	for i, want := range []float64{1.1, 1.21, 1.1} {
		if requests[i].Action != "speed" || math.Abs(requests[i].Speed-want) > 1e-9 {
			t.Fatal(requests[i])
		}
	}
	for i, want := range []int{55, 60, 55} {
		if requests[i+3].Action != "volume" || requests[i+3].Volume != want {
			t.Fatal(requests[i+3])
		}
	}
	for range 40 {
		v.key("]")
	}
	if v.status.Speed != api.MaxVideoSpeed {
		t.Fatal("speed maximum not enforced")
	}
	for range 80 {
		v.key("[")
	}
	if v.status.Speed != api.MinVideoSpeed {
		t.Fatal("speed minimum not enforced")
	}
}

func TestProgressMouseFragmentationResizeAndEndpoints(t *testing.T) {
	var out bytes.Buffer
	client, _ := video.New(&out, "secret")
	v := &viewer{out: &out, client: client, ready: true, cols: 13, rows: 8, status: api.VideoStatus{DurationMS: 10000, PositionMS: 5000}}
	p := inputParser{mouse: v.mouse}
	// Fragmented midpoint click, release, wheel, right button, drag and clicks
	// on the image/status rows. Only a left press on the track should seek.
	for _, b := range []byte("\x1b[<0;7;7M") {
		p.feed([]byte{b}, time.Now(), v.key, func(string) {})
	}
	p.feed([]byte("\x1b[<0;7;7m\x1b[<64;7;7M\x1b[<2;7;7M\x1b[<32;7;7M\x1b[<0;7;6M\x1b[<0;7;8M"), time.Now(), v.key, func(string) {})
	p.feed([]byte("\x1b[<0;1;7M\x1b[<0;13;7M"), time.Now(), v.key, func(string) {})
	requests := govRequests(t, out.String())
	if len(requests) != 3 {
		t.Fatal(requests)
	}
	for i, want := range []int64{5000, 0, 10000} {
		if requests[i].Action != "seek" || requests[i].PositionMS != want {
			t.Fatal(requests[i])
		}
	}
	if got := v.progress(); got != "[=====>-----]" {
		t.Fatal(got)
	}
	v.cols, v.rows = 7, 5
	out.Reset()
	p.feed([]byte("\x1b[<0;4;4M\x1b[<0;7;7M"), time.Now(), v.key, func(string) {})
	requests = govRequests(t, out.String())
	if len(requests) != 1 || requests[0].PositionMS != 5000 || v.bounds().Height != 2 {
		t.Fatal(requests, v.bounds())
	}
	v.status.DurationMS = 0
	out.Reset()
	p.feed([]byte("\x1b[<0;4;4M"), time.Now(), v.key, func(string) {})
	if out.Len() != 0 || v.progress() != "[-----]" {
		t.Fatal("unknown-duration seek")
	}
	v.rows = 3
	if v.progressRow() != 0 || v.bounds().Height != 1 {
		t.Fatal("small-terminal layout", v.bounds())
	}
}

func TestMalformedMouseReportsAreIgnored(t *testing.T) {
	for _, s := range []string{"[<0;0;4M", "[<0;4;0M", "[<0;-1;4M", "[<0;99999999999999999;4M", "[<0;1;2;3M", "[<0;1;2X", "[<256;1;2M"} {
		if _, ok := parseMouse(s); ok {
			t.Fatal("accepted invalid mouse report", s)
		}
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
