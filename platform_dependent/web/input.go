package web

import (
	"fmt"
	"image"
	"strconv"
	"unicode/utf8"

	"st-go/limits"
)

type inputMessage struct {
	Type      string `json:"type"`
	Key       string `json:"key"`
	Modifiers uint16 `json:"modifiers"`
	Button    byte   `json:"button"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Text      string `json:"text"`
	Focused   bool   `json:"focused"`
	ID        uint64 `json:"id"`
	OK        bool   `json:"ok"`
	Enabled   bool   `json:"enabled"`
}

// These stable key IDs match the terminal's existing configuration vocabulary;
// there is no X event, connection or keyboard mapping dependency here.
var specialKeys = map[string]uint{
	"Backspace": 0xff08, "Tab": 0xff09, "Enter": 0xff0d, "Escape": 0xff1b,
	"Delete": 0xffff, "Insert": 0xff63, "Home": 0xff50, "End": 0xff57,
	"PageUp": 0xff55, "PageDown": 0xff56, "ArrowLeft": 0xff51, "ArrowUp": 0xff52, "ArrowRight": 0xff53, "ArrowDown": 0xff54,
}

func keyID(key string, mods uint16) (uint, bool) {
	if key == "Tab" && mods&1 != 0 {
		return 0xfe20, true
	}
	if code, ok := specialKeys[key]; ok {
		return code, true
	}
	if len(key) >= 2 && key[0] == 'F' {
		n, err := strconv.Atoi(key[1:])
		if err == nil && n >= 1 && n <= 35 {
			return 0xffbe + uint(n-1), true
		}
	}
	if len(key) == 1 && key[0] >= 0x20 && key[0] <= 0x7e {
		return uint(key[0]), true
	}
	return 0, false
}

// Called only by the single controlling browser's reader. The terminal owns
// serialization; do not hold the driver mutex while invoking its callbacks.
func (d *Driver) dispatch(c *client, m inputMessage) error {
	if m.Modifiers & ^uint16(1|4|8|64) != 0 {
		return fmt.Errorf("invalid modifiers")
	}
	cb := d.callbacks
	cb.Lock()
	defer cb.Unlock()
	if cb.Closed() {
		return fmt.Errorf("terminal closed")
	}
	switch m.Type {
	case "key":
		key, ok := keyID(m.Key, m.Modifiers)
		if !ok {
			return fmt.Errorf("invalid key")
		}
		cb.KeyPress(key, m.Modifiers)
	case "text":
		if len(m.Text) > 32768 || !utf8.ValidString(m.Text) {
			return fmt.Errorf("invalid text input")
		}
		cb.Text(m.Text)
	case "paste":
		if len(m.Text) > limits.ClipboardBytes || !utf8.ValidString(m.Text) {
			return fmt.Errorf("invalid paste")
		}
		if d.ClipboardSyncState().Enabled {
			d.SetClipboard('c', m.Text)
		}
		cb.Paste(m.Text)
	case "clipboard_sync":
		d.SetClipboardSync(m.Enabled)
	case "focus":
		cb.Focus(m.Focused)
	case "resize":
		if m.Width > 65535 || m.Height > 65535 || m.Width < 2*d.window.Border+d.window.CellWidth || m.Height < 2*d.window.Border+d.window.CellHeight {
			return fmt.Errorf("invalid resize")
		}
		if _, ok := limits.RGBA("web resize", m.Width, m.Height); !ok {
			return fmt.Errorf("resize exceeds pixel budget")
		}
		if !limits.Grid((m.Width-2*d.window.Border)/d.window.CellWidth, (m.Height-2*d.window.Border)/d.window.CellHeight) {
			return fmt.Errorf("resize exceeds grid budget")
		}
		d.mu.Lock()
		if m.Width != d.width || m.Height != d.height {
			d.width, d.height = m.Width, m.Height
			d.pixels = make([]uint32, d.width*d.height)
			d.dirty = nil
			d.damage(image.Rect(0, 0, d.width, d.height))
		}
		d.mu.Unlock()
		cb.Resize(m.Width, m.Height)
	case "press", "release", "motion":
		if m.X < -65535 || m.X > 65535 || m.Y < -65535 || m.Y > 65535 {
			return fmt.Errorf("invalid pointer coordinates")
		}
		switch m.Type {
		case "motion":
			cb.PointerMotion(m.Modifiers, m.X, m.Y)
		case "press":
			if m.Button < 1 || m.Button > 11 {
				return fmt.Errorf("invalid button")
			}
			c.pressed[m.Button] = m
			cb.PointerPress(m.Button, m.Modifiers, m.X, m.Y)
		case "release":
			if m.Button < 1 || m.Button > 11 {
				return fmt.Errorf("invalid button")
			}
			delete(c.pressed, m.Button)
			cb.PointerRelease(m.Button, m.Modifiers, m.X, m.Y)
		}
	case "clipboard_reply":
		if len(m.Text) > limits.ClipboardBytes || !utf8.ValidString(m.Text) {
			return fmt.Errorf("invalid clipboard reply")
		}
		d.finishQuery(c, m.ID, m.Text, m.OK)
	case "ping":
	default:
		return fmt.Errorf("unknown input type")
	}
	cb.PaintDirty()
	return nil
}
