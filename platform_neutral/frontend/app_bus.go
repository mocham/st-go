package frontend

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A broker belongs to one native window or web session service. Delivery is
// pull-based: no cross-terminal locking or unsolicited input during shell handoff.
type appBroker struct {
	mu       sync.Mutex
	peers    map[string]*appPeer
	sequence uint64
}
type appPeer struct {
	owner    *Terminal
	channel  string
	queue    []string
	bytes    int
	touched  time.Time
	overflow bool
}

func newAppBroker() *appBroker { return &appBroker{peers: make(map[string]*appPeer)} }
func busID(s string) bool      { _, err := hex.DecodeString(s); return len(s) == 32 && err == nil }
func busChannel(s string) bool {
	if len(s) < 1 || len(s) > 96 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

// Called with the origin's parser lock held. The broker never takes a terminal
// lock or writes another terminal; replies are drained after releasing its lock.
func (t *Terminal) AppBus(args []string) {
	if t.appBroker == nil || len(args) < 2 || !busID(args[0]) {
		return
	}
	for _, control := range t.appBroker.handle(t, args, time.Now()) {
		t.termCore.WriteToTTY([]byte("\x1bPapp-bus "+args[0]+" "+control+"\x1b\\"), false)
	}
}

func (b *appBroker) handle(t *Terminal, args []string, now time.Time) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, p := range b.peers {
		if now.Sub(p.touched) > 5*time.Second {
			delete(b.peers, id)
		}
	}
	id, command := args[0], args[1]
	p := b.peers[id]
	if p != nil && p.owner != t {
		return []string{"error identity-in-use"}
	}
	if command == "subscribe" && len(args) == 3 && busChannel(args[2]) {
		created := p == nil
		if p == nil {
			if len(b.peers) >= 64 {
				return []string{"error channel-capacity"}
			}
			p = &appPeer{owner: t, channel: args[2]}
			b.peers[id] = p
		}
		if p.channel != args[2] {
			return []string{"error channel-mismatch"}
		}
		p.touched = now
		if created {
			return []string{"ready"}
		}
		return nil
	}
	if p == nil {
		return []string{"error subscription-expired"}
	}
	p.touched = now
	switch command {
	case "unsubscribe":
		delete(b.peers, id)
	case "poll":
		if p.overflow {
			p.overflow = false
			return []string{"error overflow-resynchronize"}
		}
		// Keep a reply below the PTY input capacity. Applications publish on
		// the same loop that reads replies; a larger synchronous drain can
		// deadlock both directions while the application is writing a payload.
		n := min(1, len(p.queue))
		result := append([]string(nil), p.queue[:n]...)
		for i := 0; i < n; i++ {
			p.bytes -= len(p.queue[i])
			p.queue[i] = ""
		}
		p.queue = p.queue[n:]
		return result
	case "publish":
		if len(args) != 7 || args[2] != p.channel || !busID(args[3]) {
			return []string{"error invalid-message"}
		}
		part, e1 := strconv.Atoi(args[4])
		total, e2 := strconv.Atoi(args[5])
		data, e3 := base64.RawStdEncoding.DecodeString(args[6])
		if e1 != nil || e2 != nil || e3 != nil || part < 0 || part >= total || total < 1 || total > 1024 || len(data) > 2048 {
			return []string{"error invalid-chunk"}
		}
		b.sequence++
		frame := fmt.Sprintf("message %s %d %s", id, b.sequence, strings.Join(args[2:], " "))
		for _, other := range b.peers {
			if other.channel != p.channel {
				continue
			}
			if other.overflow {
				continue
			}
			if len(other.queue) >= 2048 || other.bytes+len(frame) > 4<<20 {
				other.queue = nil
				other.bytes = 0
				other.overflow = true
				continue
			}
			other.queue = append(other.queue, frame)
			other.bytes += len(frame)
		}
	default:
		return []string{"error invalid-operation"}
	}
	return nil
}
