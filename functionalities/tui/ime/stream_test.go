package ime

import (
	"context"
	"strings"
	"testing"
	"time"

	"st-go/tui/pinyin"
)

func TestStreamTransparentInputAndDelimiters(t *testing.T) {
	for _, test := range []struct {
		input, want string
		enabled     bool
	}{
		{"a[b[[c", "a[b[[c", true},
		{"[[[raw]]]", "[[[raw]]]", false},
		{"[[[HELLO]]]x", "HELLOx", true},
		{"[[[HELLO\x1bx", "HELLOx", true},
		{"[[[A]B]]C]]]", "A]B]]C", true},
	} {
		s := Stream{Enabled: test.enabled}
		var got string
		for _, r := range test.input {
			text, used := s.Key(string(r))
			got += text
			if !used {
				got += string(r)
			}
		}
		got += s.Flush()
		if got != test.want {
			t.Errorf("%q: got %q, want %q", test.input, got, test.want)
		}
	}
	s := Stream{Enabled: true}
	for range 2 {
		s.Key("[")
	}
	text, used := s.Key("<Shortcut>")
	if text != "[[" || used {
		t.Fatalf("shortcut swallowed or reordered: %q %v", text, used)
	}
}

func TestStreamAsyncReadlineSelectionAndExit(t *testing.T) {
	result, err := pinyin.Lookup(context.Background(), pinyin.Request{ID: 1, Prompt: "ni"})
	if err != nil || len(result.Candidates) == 0 {
		t.Fatal(result, err)
	}
	wake := make(chan struct{}, 1)
	s := Stream{Enabled: true, Notify: func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}}
	defer s.Close()
	var got string
	// Input deliberately outruns dictionary results. Selection and closing
	// delimiters must stay ordered behind the async lookup, without idle polling.
	for _, ch := range "[[[ni1]]]" {
		text, used := s.Key(string(ch))
		got += text
		if !used {
			t.Fatal("composition leaked", ch)
		}
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for s.Active() {
		select {
		case <-wake:
			got += s.Poll()
		case <-deadline.C:
			t.Fatal("lookup did not wake owner")
		}
	}
	if got != result.Candidates[0] || strings.ContainsAny(got, "[]") {
		t.Fatalf("commit %q, wanted %q", got, result.Candidates[0])
	}
}
