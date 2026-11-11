package pinyin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestResponseBounds(t *testing.T) {
	for _, r := range []Request{{ID: 1, Prompt: "nh", Page: 2}, {ID: 2, Prompt: "xi'an"}} {
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []Request{{Prompt: "nh"}, {ID: 1, Prompt: strings.Repeat("a", MaxPrompt+1)}, {ID: 1, Prompt: "ni\nhao"}, {ID: 1, Prompt: "世界"}, {ID: 1, Prompt: "nh", Page: MaxPage + 1}} {
		if r.Validate() == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	request := Request{ID: 1, Prompt: "nh", Page: 2}
	response := Response{ID: 1, Prompt: "nh", Page: 2, Total: 13, Candidates: []string{"南海", "农行", "浓厚"}}
	if err := response.Validate(request); err != nil {
		t.Fatal(err)
	}
	response.Candidates[0] = "\x1b[2J"
	if response.Validate(request) == nil {
		t.Fatal("accepted candidate control text")
	}
}

func TestInProcessDecoderCompatibilityAndCrashBoundary(t *testing.T) {
	response, err := Lookup(context.Background(), Request{ID: 1, Prompt: "nh", Page: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"南海", "农行", "浓厚", "那好", "弄好"}
	if strings.Join(response.Candidates, ",") != strings.Join(want, ",") {
		t.Fatalf("nh page 3 = %q, want %q", response.Candidates, want)
	}
	for length := 1; length <= MaxPrompt; length++ {
		request := Request{ID: uint64(length + 1), Prompt: strings.Repeat("a", length)}
		response, err := Lookup(context.Background(), request)
		if err != nil {
			t.Fatalf("%d letters: %v", length, err)
		}
		if err := response.Validate(request); err != nil {
			t.Fatalf("%d letters: %v", length, err)
		}
	}
}

type testService struct {
	gate   <-chan struct{}
	exited chan struct{}
	once   sync.Once
}

func (s *testService) Lookup(ctx context.Context, r Request) (Response, error) {
	select {
	case <-s.gate:
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
	return Response{ID: r.ID, Prompt: r.Prompt, Page: 0, Total: 1, Candidates: []string{"你好"}}, nil
}
func (s *testService) Exited() <-chan struct{} { return s.exited }
func (s *testService) Close() error            { s.once.Do(func() { close(s.exited) }); return nil }

func TestWorkerCoalescesAndObservesIdleExit(t *testing.T) {
	gate := make(chan struct{})
	service := &testService{gate: gate, exited: make(chan struct{})}
	w := NewWorker(func(context.Context) (Service, error) { return service, nil })
	defer w.Close()
	for i := 1; i <= 20; i++ {
		if err := w.Submit(Request{ID: uint64(i), Prompt: "nh"}); err != nil {
			t.Fatal(err)
		}
	}
	close(gate)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case result := <-w.Results():
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			if result.Response.ID == 20 {
				goto latest
			}
		case <-deadline:
			t.Fatal("latest request was not delivered")
		}
	}
latest:
	service.Close()
	select {
	case result := <-w.Results():
		if result.Err == nil {
			t.Fatal("idle exit not reported")
		}
	case <-deadline:
		t.Fatal("idle exit blocked")
	}
}

func TestWorkerStartupFailureAndCancellation(t *testing.T) {
	w := NewWorker(func(context.Context) (Service, error) { return nil, fmt.Errorf("startup failed") })
	select {
	case result := <-w.Results():
		if result.Err == nil {
			t.Fatal("startup failure lost")
		}
	case <-time.After(time.Second):
		t.Fatal("startup blocked")
	}
	w.Close()
	<-w.Done()
	gate := make(chan struct{})
	service := &testService{gate: gate, exited: make(chan struct{})}
	w = NewWorker(func(context.Context) (Service, error) { return service, nil })
	w.Submit(Request{ID: 1, Prompt: "nh"})
	w.Close()
	select {
	case <-w.Done():
	case <-time.After(time.Second):
		t.Fatal("cancelled worker did not stop")
	}
}
