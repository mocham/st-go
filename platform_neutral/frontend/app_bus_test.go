package frontend

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestAppBusBroadcastIsolationOverflowAndLease(t *testing.T) {
	b := newAppBroker()
	a, c, other := &Terminal{}, &Terminal{}, &Terminal{}
	now := time.Now()
	idA, idC, idOther := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	b.handle(a, []string{idA, "subscribe", "project"}, now)
	b.handle(c, []string{idC, "subscribe", "project"}, now)
	b.handle(other, []string{idOther, "subscribe", "other"}, now)
	chunk := []string{idA, "publish", "project", strings.Repeat("d", 32), "0", "1", base64.RawStdEncoding.EncodeToString([]byte("hello"))}
	if got := b.handle(other, chunk, now); len(got) != 1 || got[0] != "error identity-in-use" {
		t.Fatalf("identity spoof: %v", got)
	}
	b.handle(a, chunk, now)
	for _, peer := range []struct {
		t  *Terminal
		id string
	}{{a, idA}, {c, idC}} {
		got := b.handle(peer.t, []string{peer.id, "poll"}, now)
		if len(got) != 1 || !strings.Contains(got[0], "message "+idA) {
			t.Fatalf("missing loopback or broadcast: %v", got)
		}
	}
	if got := b.handle(other, []string{idOther, "poll"}, now); len(got) != 0 {
		t.Fatal("cross-channel delivery")
	}
	for i := 0; i < 2050; i++ {
		b.handle(a, chunk, now)
	}
	if got := b.handle(c, []string{idC, "poll"}, now); len(got) != 1 || got[0] != "error overflow-resynchronize" {
		t.Fatalf("overflow: %v", got)
	}
	b.handle(c, []string{idC, "unsubscribe"}, now)
	if got := b.handle(c, []string{idC, "poll"}, now); len(got) != 1 || got[0] != "error subscription-expired" {
		t.Fatalf("unsubscribe: %v", got)
	}
	b.handle(a, []string{idA, "poll"}, now.Add(6*time.Second))
	if len(b.peers) != 0 {
		t.Fatal("expired subscriptions retained")
	}
}
