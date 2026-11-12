// Package appbus implements st's terminal-local, live broadcast channels.
// Applications poll bounded mailboxes, so data is delivered only while the
// application is actively reading terminal replies (including background tabs).
package appbus

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const ChunkBytes = 2048
const MaxMessage = 2 << 20

func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func ValidID(s string) bool { _, err := hex.DecodeString(s); return len(s) == 32 && err == nil }
func ValidChannel(s string) bool {
	if len(s) == 0 || len(s) > 96 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", c)) {
			return false
		}
	}
	return true
}

type Message struct {
	Sender, Channel string
	Sequence        uint64
	Data            []byte
}
type assembly struct {
	next, total, size int
	parts             []byte
	deadline          time.Time
}

// Client is owned by an application's event loop. All writes share that loop
// with screen drawing; asynchronous workers must return results to the loop.
type Client struct {
	ID, Channel, Token string
	Out                io.Writer
	next               time.Time
	parts              map[string]*assembly
	Ready              bool
}

func New(out io.Writer, token, channel string) (*Client, error) {
	if !ValidID(token) || !ValidChannel(channel) {
		return nil, fmt.Errorf("terminal broadcast channel unavailable or invalid")
	}
	return &Client{ID: ID(), Channel: channel, Token: token, Out: out, parts: make(map[string]*assembly)}, nil
}

func (c *Client) command(command string) error {
	data := []byte("\x1bPapp-bus auth " + c.Token + " " + c.ID + " " + command + "\x1b\\")
	for len(data) > 0 {
		n, err := c.Out.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (c *Client) Tick(now time.Time) error {
	if now.Before(c.next) {
		return nil
	}
	c.next = now.Add(150 * time.Millisecond)
	for id, a := range c.parts {
		if now.After(a.deadline) {
			delete(c.parts, id)
		}
	}
	// Subscribe is idempotent and renews the application lease after handoffs.
	if err := c.command("subscribe " + c.Channel); err != nil {
		return err
	}
	return c.command("poll")
}
func (c *Client) Close() error { return c.command("unsubscribe") }
func (c *Client) Publish(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > MaxMessage {
		return fmt.Errorf("application message exceeds 2 MiB")
	}
	id, count := ID(), (len(data)+ChunkBytes-1)/ChunkBytes
	if err := c.command("subscribe " + c.Channel); err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		part := base64.RawStdEncoding.EncodeToString(data[i*ChunkBytes : min(len(data), (i+1)*ChunkBytes)])
		if err := c.command(fmt.Sprintf("publish %s %s %d %d %s", c.Channel, id, i, count, part)); err != nil {
			return err
		}
	}
	return nil
}

// Receive consumes only app-bus controls. A complete payload is returned once;
// discontinuous/chunk-missing messages are discarded, never partly delivered.
func (c *Client) Receive(control string) (*Message, error) {
	f := strings.Fields(control)
	if len(f) < 3 || f[0] != "app-bus" || f[1] != c.ID {
		return nil, nil
	}
	if f[2] == "ready" {
		c.Ready = true
		return nil, nil
	}
	if f[2] == "error" {
		clear(c.parts)
		return nil, fmt.Errorf("terminal channel: %s", strings.Join(f[3:], " "))
	}
	if len(f) != 10 || f[2] != "message" || f[5] != c.Channel || !ValidID(f[3]) || !ValidID(f[6]) {
		return nil, nil
	}
	// Drain an active mailbox on the next event-loop turn. Idle mailboxes
	// retain the normal heartbeat interval; at most one chunk is in flight.
	c.next = time.Time{}
	i, e1 := strconv.Atoi(f[7])
	sequence, e4 := strconv.ParseUint(f[4], 10, 64)
	total, e2 := strconv.Atoi(f[8])
	data, e3 := base64.RawStdEncoding.DecodeString(f[9])
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || sequence == 0 || i < 0 || i >= total || total < 1 || total > MaxMessage/ChunkBytes || len(data) > ChunkBytes {
		return nil, fmt.Errorf("invalid channel chunk")
	}
	key := f[3] + ":" + f[6]
	if i == 0 {
		if len(c.parts) >= 16 {
			return nil, fmt.Errorf("too many channel messages")
		}
		c.parts[key] = &assembly{total: total, deadline: time.Now().Add(30 * time.Second)}
	}
	a := c.parts[key]
	if a == nil {
		return nil, nil
	}
	if a.next != i || a.total != total || a.size+len(data) > MaxMessage {
		delete(c.parts, key)
		return nil, fmt.Errorf("incomplete channel message")
	}
	a.parts = append(a.parts, data...)
	a.deadline = time.Now().Add(30 * time.Second)
	a.size += len(data)
	a.next++
	if a.next != total {
		return nil, nil
	}
	delete(c.parts, key)
	return &Message{Sender: f[3], Channel: f[5], Sequence: sequence, Data: a.parts}, nil
}
