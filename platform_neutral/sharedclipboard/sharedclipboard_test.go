package sharedclipboard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("clipboard state did not converge")
}

func query(t *testing.T, sync *Sync, selection byte) (string, bool) {
	t.Helper()
	reply := make(chan struct {
		text string
		ok   bool
	}, 1)
	sync.Query(selection, func(text string, ok bool) {
		reply <- struct {
			text string
			ok   bool
		}{text, ok}
	})
	select {
	case result := <-reply:
		return result.text, result.ok
	case <-time.After(time.Second):
		t.Fatal("clipboard query blocked")
		return "", false
	}
}

func TestDisabledSyncDoesNotCreateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.sqlite3")
	sync := New(path, Hooks{})
	sync.Publish('c', "ignored", SourceLocal, "")
	if _, ok := query(t, sync, 'c'); ok {
		t.Fatal("disabled clipboard returned shared state")
	}
	sync.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("disabled clipboard created the cache database", err)
	}
}

func TestIndependentPeersShareBoundedClipboardState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.sqlite3")
	changes := make(chan Entry, 8)
	first := New(path, Hooks{})
	second := New(path, Hooks{Changed: func(entry Entry) { changes <- entry }})
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	if !first.SetEnabled(true, map[byte]string{'p': "seed"}) || !second.SetEnabled(true, nil) {
		t.Fatal("shared clipboard was unavailable")
	}
	await(t, func() bool { return first.State().Ready && second.State().Ready })
	first.Publish('c', "copied 世界", SourceLocal, "")
	second.SetWatching(true)
	await(t, func() bool {
		text, ok := query(t, second, 'c')
		return ok && text == "copied 世界"
	})
	first.Publish('c', "newer", SourceXExternal, ":1")
	await(t, func() bool {
		select {
		case entry := <-changes:
			return entry.Selection == 'c' && entry.Text == "newer" && entry.Source == SourceXExternal && entry.Display == ":1"
		default:
			return false
		}
	})
	if text, ok := query(t, first, 'p'); !ok || text != "seed" {
		t.Fatal("seeded PRIMARY was lost", text, ok)
	}
}

func TestDisablingStopsDatabaseParticipation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.sqlite3")
	sync := New(path, Hooks{})
	t.Cleanup(sync.Close)
	sync.SetEnabled(true, nil)
	await(t, func() bool { return sync.State().Ready })
	sync.Publish('c', "before", SourceLocal, "")
	await(t, func() bool { text, ok := query(t, sync, 'c'); return ok && text == "before" })
	sync.SetEnabled(false, nil)
	await(t, func() bool { return !sync.State().Ready })
	if _, ok := query(t, sync, 'c'); ok {
		t.Fatal("disabled peer continued reading shared clipboard")
	}
}
