package main

import (
	"bytes"
	"testing"
)

func TestSharedReadlinePromptCursor(t *testing.T) {
	var out bytes.Buffer
	v := &viewer{out: &out, cols: 40}
	v.key("/")
	for _, key := range []string{"w", "r", "l", "d", "\x01", "right", "o"} {
		v.key(key)
	}
	if v.prompt != "/world" {
		t.Fatalf("cursor editing=%q", v.prompt)
	}
	v.key("\r")
	if v.search != "world" {
		t.Fatal("edited search was not submitted")
	}
	v.key(":")
	for _, key := range []string{"a", "b", "\x01", "X", "\x05", "Y"} {
		v.key(key)
	}
	if v.prompt != ":XabY" {
		t.Fatalf("Ctrl-A/E=%q", v.prompt)
	}
}
