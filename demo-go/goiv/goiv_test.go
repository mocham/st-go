package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOptionsAndSiblingImages(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.JPG", "a.png", "notes.txt", "c.webp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	o, err := parseOptions([]string{filepath.Join(dir, "b.JPG")}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	images, index, err := imageFiles(o.file)
	if err != nil || index != 1 {
		t.Fatalf("images=%v index=%d err=%v", images, index, err)
	}
	want := []string{filepath.Join(dir, "a.png"), filepath.Join(dir, "b.JPG"), filepath.Join(dir, "c.webp")}
	if !reflect.DeepEqual(images, want) {
		t.Fatalf("images=%v want=%v", images, want)
	}
	for _, args := range [][]string{{}, {dir}, {filepath.Join(dir, "notes.txt")}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	control := filepath.Join(dir, "bad\nname.png")
	if err := os.WriteFile(control, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseOptions([]string{control}, io.Discard); err == nil {
		t.Fatal("control-containing path was accepted into the DCS protocol")
	}
}

func TestBindingsNavigationAndSafeProtocol(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a ';one.png", "b two.webp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	v, err := newViewer(options{file: filepath.Join(dir, "a ';one.png")}, &out)
	if err != nil {
		t.Fatal(err)
	}
	v.open()
	if !strings.Contains(out.String(), "'\"'\"'") || !strings.Contains(out.String(), "viewer fit-width") {
		t.Fatalf("unsafe or missing open command: %q", out.String())
	}
	for _, test := range []struct {
		mode scaleMode
		want string
	}{{fitWidth, "viewer fit-width"}, {fitHeight, "viewer fit-height"}, {originalSize, "viewer native"}, {customZoom, "viewer zoom 175"}} {
		out.Reset()
		v.mode, v.zoom = test.mode, 175
		v.open()
		if strings.Count(out.String(), "\x1bP") != 1 || !strings.Contains(out.String(), test.want) {
			t.Errorf("mode %d did not produce one direct open: %q", test.mode, out.String())
		}
	}
	out.Reset()
	v.mode, v.zoom = fitWidth, 100
	for _, key := range []string{"a", "o", "w", "ctrl-plus", "ctrl-minus", "down", "left", "pagedown", "n", "p"} {
		v.key(key)
	}
	for _, command := range []string{"fit height", "fit original", "fit width", "zoomstep 1", "zoomstep -1", "scroll 1", "pan -1", "view 1000000"} {
		if !strings.Contains(out.String(), command) {
			t.Errorf("missing %q in %q", command, out.String())
		}
	}
	if v.index != 0 {
		t.Fatal("next/previous did not wrap to the original image")
	}
}

func TestFragmentedInputAndStatus(t *testing.T) {
	var parser inputParser
	var keys, replies []string
	input := "w\x1b[43;5u\x1b[45;5u\x1bPregion secondary status 1 1 250000 125 0 0\x1b\\"
	for _, b := range []byte(input) {
		parser.feed([]byte{b}, time.Now(), func(k string) { keys = append(keys, k) }, func(r string) { replies = append(replies, r) })
	}
	if !reflect.DeepEqual(keys, []string{"w", "ctrl-plus", "ctrl-minus"}) || len(replies) != 1 {
		t.Fatalf("keys=%v replies=%v", keys, replies)
	}
	v := &viewer{zoom: 100}
	v.reply(replies[0])
	if v.position != 250000 || v.zoom != 125 || v.loading || v.failed {
		t.Fatalf("status=%+v", v)
	}
	v.reply("region secondary image-status 1920 1080 500000 125 zoom 0 0")
	if v.position != 500000 || v.zoom != 125 || v.mode != customZoom {
		t.Fatalf("image status=%+v", v)
	}
	if !isLayoutReply("region layout main 1 1 80 1 secondary 1 2 80 23") || isLayoutReply("region layout main 1 1 zero 1 secondary 1 2 zero 23") {
		t.Fatal("layout reply validation failed")
	}
}
