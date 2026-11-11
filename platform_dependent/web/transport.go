package web

import (
	"image"
	"time"
	"unicode/utf8"

	"golang.org/x/net/websocket"
	"st-go/limits"
	"st-go/platform_neutral/sharedclipboard"
)

type serverMessage struct {
	Overlays      []htmlOverlay `json:"overlays,omitempty"`
	Type          string        `json:"type"`
	Width         int           `json:"width,omitempty"`
	Height        int           `json:"height,omitempty"`
	CellWidth     int           `json:"cellWidth,omitempty"`
	CellHeight    int           `json:"cellHeight,omitempty"`
	Border        int           `json:"border,omitempty"`
	Selection     string        `json:"selection,omitempty"`
	Text          string        `json:"text,omitempty"`
	ID            uint64        `json:"id,omitempty"`
	SyncEnabled   bool          `json:"syncEnabled,omitempty"`
	SyncReady     bool          `json:"syncReady,omitempty"`
	SyncAvailable bool          `json:"syncAvailable,omitempty"`
}
type client struct {
	conn     *websocket.Conn
	controls chan serverMessage
	pressed  map[byte]inputMessage
	done     chan struct{}
	paintAck chan uint64
}
type clipboardQuery struct {
	id     uint64
	client *client
	reply  func(string, bool)
	timer  *time.Timer
}

func (d *Driver) serveSocket(conn *websocket.Conn) {
	// JSON may escape each byte of valid clipboard text as six ASCII bytes.
	conn.MaxPayloadBytes = 6*limits.ClipboardBytes + 16384
	c := &client{conn: conn, controls: make(chan serverMessage, 4), pressed: make(map[byte]inputMessage), done: make(chan struct{}), paintAck: make(chan uint64, 16)}
	d.mu.Lock()
	select {
	case <-d.done:
		d.mu.Unlock()
		conn.Close()
		return
	default:
	}
	d.client = c
	if d.clipboardSync != nil {
		d.clipboardSync.SetWatching(true)
	}
	d.damage(image.Rect(0, 0, d.width, d.height))
	state := d.ClipboardSyncState()
	hello := serverMessage{Type: "hello", Width: d.width, Height: d.height, CellWidth: d.window.CellWidth, CellHeight: d.window.CellHeight, Border: d.window.Border,
		SyncEnabled: state.Enabled, SyncReady: state.Ready, SyncAvailable: state.Available}
	d.mu.Unlock()
	writerDone := make(chan struct{})
	go func() { defer close(writerDone); defer conn.Close(); d.writeSocket(c, hello) }()
	defer func() {
		close(c.done)
		conn.Close()
		<-writerDone
		d.mu.Lock()
		if d.client == c {
			d.client = nil
			if d.clipboardSync != nil {
				d.clipboardSync.SetWatching(false)
			}
		}
		var queryID uint64
		if d.query != nil && d.query.client == c {
			queryID = d.query.id
		}
		d.mu.Unlock()
		cb := d.callbacks
		cb.Lock()
		if queryID != 0 {
			d.finishQuery(c, queryID, "", false)
		}
		if !cb.Closed() {
			for button, m := range c.pressed {
				cb.PointerRelease(button, m.Modifiers, m.X, m.Y)
			}
			cb.Focus(false)
			cb.PaintDirty()
		}
		cb.Unlock()
	}()
	for {
		var input inputMessage
		// Expire vanished controllers so a broken network cannot reserve the
		// single attachment forever. The embedded browser sends an idle heartbeat.
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		if err := websocket.JSON.Receive(conn, &input); err != nil {
			return
		}
		if err := d.dispatch(c, input); err != nil {
			return
		}
	}
}

func sendJSON(c *client, message serverMessage) error {
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return websocket.JSON.Send(c.conn, message)
}

func (d *Driver) writeSocket(c *client, hello serverMessage) {
	if sendJSON(c, hello) != nil {
		return
	}
	glyphs := newGlyphEncoder(hello.CellWidth, hello.CellHeight, hello.Border)
	var clipboardVersion uint64
	var syncVersion uint64
	var lastFrame time.Time
	var sequence, pending uint64
	var flowControlled bool
	var htmlVersion uint64
	// Repaint immediately on attachment, then coalesce bursts at frame cadence.
	ticker := time.NewTicker(limits.PreviewFrameInterval)
	defer ticker.Stop()
	for {
		select {
		case <-d.done:
			return
		case <-c.done:
			return
		case control := <-c.controls:
			if sendJSON(c, control) != nil {
				return
			}
			continue
		case id := <-c.paintAck:
			flowControlled = true
			if id == pending {
				pending = 0
			}
		case <-d.wake:
		case <-ticker.C:
		}
		d.mu.Lock()
		version, message := d.clipboardVersion, d.clipboardUpdate
		statusVersion, statusMessage := d.syncVersion, d.syncUpdate
		d.mu.Unlock()
		if statusVersion != syncVersion {
			if sendJSON(c, statusMessage) != nil {
				return
			}
			syncVersion = statusVersion
		}
		if version != clipboardVersion {
			if sendJSON(c, message) != nil {
				return
			}
			clipboardVersion = version
		}
		if flowControlled && pending != 0 || time.Since(lastFrame) < limits.PreviewFrameInterval {
			continue
		}
		// Snapshot only at a terminal transaction boundary, so a resize or a
		// multi-span synchronized paint cannot reach the browser half committed.
		d.callbacks.Lock()
		frames := d.frames()
		version, html := d.htmlSnapshot(htmlVersion)
		d.callbacks.Unlock()
		if html != nil {
			if sendJSON(c, *html) != nil {
				return
			}
			htmlVersion = version
		}
		budget := maxGlyphDefinitions
		for _, frame := range frames {
			lastFrame = time.Now()
			messages, err := glyphs.encodeWithBudget(frame, &budget)
			if err != nil {
				return
			}
			for _, data := range messages {
				c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if websocket.Message.Send(c.conn, data) != nil {
					return
				}
			}
		}
		if len(frames) != 0 || html != nil {
			sequence++
			pending = sequence
			if sendJSON(c, serverMessage{Type: "paint", ID: sequence}) != nil {
				return
			}
		}
		// A disconnected client closes the writer through the underlying socket.
		// No terminal/driver locks are held during encoding or network writes.
	}
}

func (d *Driver) SetClipboard(selection byte, text string) {
	if selection != 'c' && selection != 'p' || len(text) > limits.ClipboardBytes || !utf8.ValidString(text) {
		return
	}
	d.mu.Lock()
	d.clipboard[selection] = text
	d.clipboardVersion++
	d.clipboardUpdate = serverMessage{Type: "clipboard", Selection: string(selection), Text: text}
	d.mu.Unlock()
	if d.clipboardSync != nil {
		d.clipboardSync.Publish(selection, text, sharedclipboard.SourceLocal, "")
	}
	d.notify()
}

func (d *Driver) QueryClipboard(selection byte, reply func(string, bool)) {
	if selection != 'c' && selection != 'p' {
		reply("", false)
		return
	}
	if d.clipboardSync != nil && d.clipboardSync.State().Enabled {
		d.clipboardSync.Query(selection, func(text string, ok bool) {
			if ok {
				reply(text, true)
				return
			}
			d.mu.Lock()
			text, ok = d.clipboard[selection]
			d.mu.Unlock()
			reply(text, ok)
		})
		return
	}
	d.mu.Lock()
	if text, ok := d.clipboard[selection]; selection == 'p' && ok {
		d.mu.Unlock()
		reply(text, true)
		return
	}
	c := d.client
	if c == nil || d.query != nil {
		d.mu.Unlock()
		reply("", false)
		return
	}
	d.nextQuery++
	q := &clipboardQuery{id: d.nextQuery, client: c, reply: reply}
	q.timer = time.AfterFunc(1500*time.Millisecond, func() {
		d.callbacks.Lock()
		defer d.callbacks.Unlock()
		d.finishQuery(c, q.id, "", false)
	})
	d.query = q
	select {
	case c.controls <- serverMessage{Type: "clipboard_read", ID: q.id, Selection: string(selection)}:
		d.mu.Unlock()
	default:
		d.query = nil
		q.timer.Stop()
		d.mu.Unlock()
		reply("", false)
	}
}

// Caller holds the terminal lock. A stale/disconnected browser cannot answer a
// later request, and every accepted request finishes at most once.
func (d *Driver) finishQuery(c *client, id uint64, text string, ok bool) {
	d.mu.Lock()
	q := d.query
	if q == nil || q.client != c || q.id != id {
		d.mu.Unlock()
		return
	}
	d.query = nil
	q.timer.Stop()
	d.mu.Unlock()
	if !d.callbacks.Closed() {
		q.reply(text, ok)
	}
}

func (d *Driver) RequestPaste() {
	d.QueryClipboard('c', func(text string, ok bool) {
		if ok {
			d.callbacks.Paste(text)
		}
	})
}
