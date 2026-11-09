package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const maxClipboardBytes = 8 << 20
const maxClipboardControl = (maxClipboardBytes+2)/3*4 + 64
const clipboardTimeout = 2 * time.Second

// The application only speaks OSC 52. Selection ownership, desktop APIs and
// persistence belong to the hosting terminal, not this TUI.
type terminalClipboard struct {
	out    io.Writer
	frames []clipboardFrame
	bytes  int
}
type clipboardFrame struct {
	selection byte
	query     bool
	data      string
}
type clipboardReply struct {
	selection byte
	text      string
	err       error
}
type clipboardRead struct {
	selection byte
	resume    func(register)
	deadline  time.Time
	queued    []keyEvent
	bytes     int
}

func clipboardSelection(primary bool) byte {
	if primary {
		return 'p'
	}
	return 'c'
}
func (c *terminalClipboard) enqueue(frame clipboardFrame) error {
	replace := len(c.frames) > 0 && !frame.query && !c.frames[len(c.frames)-1].query && c.frames[len(c.frames)-1].selection == frame.selection
	size := c.bytes + len(frame.data)
	if replace {
		size -= len(c.frames[len(c.frames)-1].data)
	}
	if size > 2*maxClipboardControl || !replace && len(c.frames) >= 1024 {
		return fmt.Errorf("terminal clipboard output queue is full")
	}
	if replace {
		c.frames[len(c.frames)-1] = frame
	} else {
		c.frames = append(c.frames, frame)
	}
	c.bytes = size
	return nil
}
func (c *terminalClipboard) Set(text string, primary bool) error {
	if len(text) > maxClipboardBytes || !utf8.ValidString(text) {
		return fmt.Errorf("clipboard exceeds 8 MiB or is not UTF-8")
	}
	sel := clipboardSelection(primary)
	return c.enqueue(clipboardFrame{selection: sel, data: fmt.Sprintf("\x1b]52;%c;%s\x1b\\", sel, base64.StdEncoding.EncodeToString([]byte(text)))})
}
func (c *terminalClipboard) Request(primary bool) error {
	sel := clipboardSelection(primary)
	return c.enqueue(clipboardFrame{selection: sel, query: true, data: fmt.Sprintf("\x1b]52;%c;?\x1b\\", sel)})
}
func (c *terminalClipboard) Flush() error {
	for len(c.frames) > 0 {
		if err := writeAll(c.out, []byte(c.frames[0].data)); err != nil {
			return err
		}
		c.bytes -= len(c.frames[0].data)
		c.frames[0] = clipboardFrame{}
		c.frames = c.frames[1:]
	}
	c.frames = nil
	return nil
}
func (c *terminalClipboard) Close() error { return c.Flush() }

func parseClipboardReply(value []byte) *clipboardReply {
	parts := strings.SplitN(string(value), ";", 3)
	if len(parts) != 3 || parts[0] != "52" || len(parts[1]) != 1 || !strings.ContainsAny(parts[1], "cp") || parts[2] == "?" {
		return nil
	}
	result := &clipboardReply{selection: parts[1][0]}
	if len(parts[2]) > base64.StdEncoding.EncodedLen(maxClipboardBytes) {
		result.err = fmt.Errorf("terminal clipboard exceeds 8 MiB")
		return result
	}
	data, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || len(data) > maxClipboardBytes || !utf8.Valid(data) {
		result.err = fmt.Errorf("invalid terminal clipboard reply")
		return result
	}
	result.text = string(data)
	return result
}

func (e *Editor) withRegister(name rune, resume func(register)) {
	if name != '+' && name != '*' {
		resume(e.getRegister(name))
		return
	}
	if e.clip == nil {
		e.error(fmt.Errorf("terminal clipboard unavailable; internal registers are separate"))
		return
	}
	if err := e.clip.Request(name == '*'); err != nil {
		e.error(err)
		return
	}
	e.clipboardRead = &clipboardRead{selection: clipboardSelection(name == '*'), resume: resume, deadline: time.Now().Add(clipboardTimeout)}
	e.message = "Waiting for terminal clipboard (Esc cancels)"
}
func (e *Editor) finishClipboard(reply *clipboardReply) {
	read := e.clipboardRead
	if read == nil || read.selection != reply.selection {
		return
	}
	e.clipboardRead = nil
	if reply.err != nil {
		e.error(reply.err)
	} else {
		name := '+'
		if read.selection == 'p' {
			name = '*'
		}
		value := e.registers[name]
		if value.Text != reply.text {
			value = register{Text: reply.text, Line: strings.HasSuffix(reply.text, "\n")}
		}
		e.registers[name] = value
		e.message = ""
		read.resume(value)
	}
	e.finishCommand()
	for _, event := range read.queued {
		e.Feed(event)
	}
}
