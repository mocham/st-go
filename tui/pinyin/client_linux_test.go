package pinyin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func builtHelper(t *testing.T) string {
	t.Helper()
	path := os.Getenv("GOVIM_PINYIN_HELPER")
	if path == "" {
		path = "../../BINARIES/govim-pinyind"
	}
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0111 == 0 {
		t.Skip("run make govim-pinyind for native integration")
	}
	return path
}
func TestNativePinyinExampleAndPrivateSocket(t *testing.T) {
	client, err := Start(context.Background(), builtHelper(t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	response, err := client.Lookup(context.Background(), Request{ID: 1, Prompt: "nh", Page: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Candidates) != 5 || response.Candidates[2] != "浓厚" {
		t.Fatalf("nh++3 = %+v", response)
	}
	previous, err := client.Lookup(context.Background(), Request{ID: 2, Prompt: "nh", Page: 1})
	if err != nil || previous.Candidates[2] == response.Candidates[2] {
		t.Fatalf("previous page = %+v %v", previous, err)
	}
	if !strings.HasPrefix(client.conn.RemoteAddr().String(), "@govim-pinyin-") {
		t.Fatal("socket has a filesystem path")
	}
	if _, err := client.Lookup(context.Background(), Request{ID: 3, Prompt: strings.Repeat("a", MaxPrompt+1)}); err == nil {
		t.Fatal("oversized prompt reached native code")
	}
}
func TestNativePinyinCrashIsIsolatedAndFreshSessionWorks(t *testing.T) {
	helper := builtHelper(t)
	client, err := Start(context.Background(), helper)
	if err != nil {
		t.Fatal(err)
	}
	// The pinned upstream decoder actually aborts on this prompt. It is sent
	// only to a disposable child, never to code linked into the test/editor.
	_, err = client.Lookup(context.Background(), Request{ID: 1, Prompt: strings.Repeat("a", 32)})
	if err == nil {
		client.Close()
		t.Fatal("expected upstream crash regression was not exercised")
	}
	client.Close()
	select {
	case <-client.Exited():
	case <-time.After(2 * time.Second):
		t.Fatal("crashed helper not reaped")
	}
	fresh, err := Start(context.Background(), helper)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	response, err := fresh.Lookup(context.Background(), Request{ID: 1, Prompt: "nihao"})
	if err != nil || response.Candidates[0] != "你好" {
		t.Fatalf("fresh helper failed after native crash: %+v %v", response, err)
	}
}
