package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
)

const maxClipboardBytes = 8 << 20

type clipboardResult struct {
	text string
	err  error
}
type clipboardTransfer struct {
	text    string
	offset  int
	target  xproto.Atom
	started time.Time
}
type transferID struct {
	window   xproto.Window
	property xproto.Atom
}
type xClipboard struct {
	conn            *xgb.Conn
	window          xproto.Window
	atoms           map[string]xproto.Atom
	mu              sync.Mutex
	requestMu       sync.Mutex
	text, primary   string
	own, ownPrimary bool
	pending         chan clipboardResult
	incoming        []byte
	incr            bool
	incomingType    xproto.Atom
	transfers       map[transferID]*clipboardTransfer
	done            chan struct{}
	keep            bool
	closed          bool
}

func newXClipboard(keep bool) (*xClipboard, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("X11 clipboard unavailable: %w", err)
	}
	c := &xClipboard{conn: conn, atoms: make(map[string]xproto.Atom), transfers: make(map[transferID]*clipboardTransfer), done: make(chan struct{}), keep: keep}
	for _, name := range []string{"CLIPBOARD", "PRIMARY", "UTF8_STRING", "STRING", "TARGETS", "ATOM", "INCR", "GOVIM_CLIPBOARD"} {
		reply, err := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply()
		if err != nil {
			conn.Close()
			return nil, err
		}
		c.atoms[name] = reply.Atom
	}
	window, err := xproto.NewWindowId(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	c.window = window
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	if err := xproto.CreateWindowChecked(conn, 0, window, screen.Root, 0, 0, 1, 1, 0, xproto.WindowClassInputOnly, 0, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange}).Check(); err != nil {
		conn.Close()
		return nil, err
	}
	go c.loop()
	return c, nil
}
func (c *xClipboard) Set(text string, primary bool) error {
	if len(text) > maxClipboardBytes {
		return fmt.Errorf("clipboard exceeds 8 MiB")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("clipboard is closed")
	}
	selection := c.atoms["CLIPBOARD"]
	if primary {
		selection = c.atoms["PRIMARY"]
	}
	if err := xproto.SetSelectionOwnerChecked(c.conn, c.window, selection, xproto.TimeCurrentTime).Check(); err != nil {
		return err
	}
	owner, err := xproto.GetSelectionOwner(c.conn, selection).Reply()
	if err != nil {
		return err
	}
	if owner.Owner != c.window {
		return fmt.Errorf("could not own X11 selection")
	}
	if primary {
		c.primary, c.ownPrimary = text, true
	} else {
		c.text, c.own = text, true
	}
	return nil
}
func (c *xClipboard) Get(primary bool) (string, error) {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	selection := c.atoms["CLIPBOARD"]
	if primary {
		selection = c.atoms["PRIMARY"]
	}
	for _, target := range []xproto.Atom{c.atoms["UTF8_STRING"], c.atoms["STRING"]} {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return "", fmt.Errorf("clipboard is closed")
		}
		owner, err := xproto.GetSelectionOwner(c.conn, selection).Reply()
		if err != nil {
			c.mu.Unlock()
			return "", err
		}
		if owner.Owner == c.window {
			text := c.text
			if primary {
				text = c.primary
			}
			c.mu.Unlock()
			return text, nil
		}
		pending := make(chan clipboardResult, 1)
		c.pending = pending
		c.incoming = nil
		c.incr = false
		xproto.DeleteProperty(c.conn, c.window, c.atoms["GOVIM_CLIPBOARD"])
		xproto.ConvertSelection(c.conn, c.window, selection, target, c.atoms["GOVIM_CLIPBOARD"], xproto.TimeCurrentTime)
		c.mu.Unlock()
		select {
		case result := <-pending:
			if result.err == nil {
				return result.text, nil
			}
		case <-time.After(2 * time.Second):
			c.mu.Lock()
			if c.pending == pending {
				c.pending = nil
				c.incoming = nil
				c.incr = false
			}
			c.mu.Unlock()
			return "", fmt.Errorf("clipboard transfer timed out")
		case <-c.done:
			return "", fmt.Errorf("clipboard connection closed")
		}
	}
	return "", fmt.Errorf("clipboard owner does not provide text")
}
func (c *xClipboard) finishGet(text string, err error) {
	if c.pending != nil {
		select {
		case c.pending <- clipboardResult{text, err}:
		default:
		}
		c.pending = nil
	}
	c.incr = false
	c.incoming = nil
}
func (c *xClipboard) loop() {
	defer close(c.done)
	emptyEvents := 0
	for {
		event, err := c.conn.WaitForEvent()
		if err != nil { // A requestor can disappear midway through INCR.
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if closed {
				return
			}
			continue
		}
		if event == nil {
			c.mu.Lock()
			closed := c.closed
			// This XGB revision also returns nil/nil for unknown events. Ignore
			// one, but consecutive empty blocking reads mean its event channel
			// is closed. XGB has already closed its request channel in that case.
			emptyEvents++
			if emptyEvents >= 2 && !closed {
				c.closed = true
				c.own, c.ownPrimary = false, false
				c.finishGet("", fmt.Errorf("clipboard connection closed"))
				closed = true
			}
			c.mu.Unlock()
			if closed {
				return
			}
			continue
		}
		emptyEvents = 0
		c.mu.Lock()
		switch ev := event.(type) {
		case xproto.SelectionRequestEvent:
			c.request(ev)
		case xproto.SelectionClearEvent:
			if ev.Selection == c.atoms["CLIPBOARD"] {
				c.own = false
			}
			if ev.Selection == c.atoms["PRIMARY"] {
				c.ownPrimary = false
			}
			if !c.keep && !c.own && !c.ownPrimary {
				c.closed = true
				c.conn.Close()
			}
		case xproto.SelectionNotifyEvent:
			if c.pending == nil || ev.Requestor != c.window {
				break
			}
			if ev.Property == 0 {
				c.finishGet("", fmt.Errorf("unsupported clipboard target"))
				break
			}
			property, err := xproto.GetProperty(c.conn, false, c.window, ev.Property, xproto.GetPropertyTypeAny, 0, maxClipboardBytes/4+1).Reply()
			if err != nil {
				c.finishGet("", err)
				break
			}
			if property.Type == c.atoms["INCR"] {
				c.incr = true
				c.incomingType = ev.Target
				c.incoming = nil
				if len(property.Value) >= 4 && binary.LittleEndian.Uint32(property.Value) > maxClipboardBytes {
					c.finishGet("", fmt.Errorf("clipboard exceeds 8 MiB"))
				}
			} else if property.BytesAfter > 0 || len(property.Value) > maxClipboardBytes {
				c.finishGet("", fmt.Errorf("clipboard exceeds 8 MiB"))
			} else {
				text, err := c.decodeText(property.Type, property.Format, property.Value)
				c.finishGet(text, err)
			}
			xproto.DeleteProperty(c.conn, c.window, ev.Property)
		case xproto.PropertyNotifyEvent:
			if ev.Window == c.window && ev.Atom == c.atoms["GOVIM_CLIPBOARD"] && ev.State == xproto.PropertyNewValue && c.incr && c.pending != nil {
				property, err := xproto.GetProperty(c.conn, true, c.window, ev.Atom, xproto.GetPropertyTypeAny, 0, uint32((maxClipboardBytes-len(c.incoming))/4+1)).Reply()
				if err != nil {
					c.finishGet("", err)
					break
				}
				if property.BytesAfter > 0 || len(c.incoming)+len(property.Value) > maxClipboardBytes {
					c.finishGet("", fmt.Errorf("clipboard exceeds 8 MiB"))
					xproto.DeleteProperty(c.conn, c.window, ev.Atom)
					break
				}
				if len(property.Value) == 0 {
					text, err := c.decodeText(c.incomingType, 8, c.incoming)
					c.finishGet(text, err)
				} else {
					if property.Format != 8 || property.Type != c.incomingType {
						c.finishGet("", fmt.Errorf("invalid clipboard chunk"))
						break
					}
					c.incoming = append(c.incoming, property.Value...)
				}
			} else if ev.State == xproto.PropertyDelete {
				if transfer := c.transfers[transferID{ev.Window, ev.Atom}]; transfer != nil {
					c.sendChunk(transferID{ev.Window, ev.Atom}, transfer)
				}
			}
		}
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return
		}
	}
}

func (c *xClipboard) request(ev xproto.SelectionRequestEvent) {
	property := ev.Property
	if property == 0 {
		property = ev.Target
	}
	success := false
	notify := func() {
		prop := xproto.Atom(0)
		if success {
			prop = property
		}
		xproto.SendEvent(c.conn, false, ev.Requestor, 0, string(xproto.SelectionNotifyEvent{Time: ev.Time, Requestor: ev.Requestor, Selection: ev.Selection, Target: ev.Target, Property: prop}.Bytes()))
	}
	if ev.Selection != c.atoms["CLIPBOARD"] && ev.Selection != c.atoms["PRIMARY"] {
		notify()
		return
	}
	text := c.text
	if ev.Selection == c.atoms["PRIMARY"] {
		text = c.primary
	}
	if ev.Target == c.atoms["STRING"] {
		text = latin1Text(text)
	}
	if ev.Target == c.atoms["TARGETS"] {
		targets := []xproto.Atom{c.atoms["TARGETS"], c.atoms["UTF8_STRING"], c.atoms["STRING"]}
		data := make([]byte, len(targets)*4)
		for i, target := range targets {
			binary.LittleEndian.PutUint32(data[i*4:], uint32(target))
		}
		xproto.ChangeProperty(c.conn, xproto.PropModeReplace, ev.Requestor, property, c.atoms["ATOM"], 32, uint32(len(targets)), data)
		success = true
		notify()
		return
	}
	if ev.Target != c.atoms["UTF8_STRING"] && ev.Target != c.atoms["STRING"] {
		notify()
		return
	}
	for id, transfer := range c.transfers {
		if time.Since(transfer.started) > 10*time.Second {
			delete(c.transfers, id)
		}
	}
	if len(text) > 32768 {
		if len(c.transfers) >= 4 {
			notify()
			return
		}
		id := transferID{ev.Requestor, property}
		c.transfers[id] = &clipboardTransfer{text: text, target: ev.Target, started: time.Now()}
		xproto.ChangeWindowAttributes(c.conn, ev.Requestor, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange})
		var length [4]byte
		binary.LittleEndian.PutUint32(length[:], uint32(len(text)))
		xproto.ChangeProperty(c.conn, xproto.PropModeReplace, ev.Requestor, property, c.atoms["INCR"], 32, 1, length[:])
		success = true
		notify()
		return
	}
	xproto.ChangeProperty(c.conn, xproto.PropModeReplace, ev.Requestor, property, ev.Target, 8, uint32(len(text)), []byte(text))
	success = true
	notify()
}
func (c *xClipboard) sendChunk(id transferID, transfer *clipboardTransfer) {
	end := min(len(transfer.text), transfer.offset+32768)
	chunk := transfer.text[transfer.offset:end]
	xproto.ChangeProperty(c.conn, xproto.PropModeReplace, id.window, id.property, transfer.target, 8, uint32(len(chunk)), []byte(chunk))
	if len(chunk) == 0 {
		delete(c.transfers, id)
	} else {
		transfer.offset = end
		transfer.started = time.Now()
	}
}

func (c *xClipboard) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	text, primary := "", ""
	if c.own {
		text = c.text
	}
	if c.ownPrimary {
		primary = c.primary
	}
	keep := c.keep
	c.mu.Unlock()
	var err error
	if keep && (text != "" || primary != "") {
		err = persistClipboard(text, primary)
	}
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		xproto.DestroyWindow(c.conn, c.window)
		c.conn.Close()
	}
	c.mu.Unlock()
	select {
	case <-c.done:
	case <-time.After(time.Second):
	}
	return err
}

func persistClipboard(text, primary string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if len(text) > maxClipboardBytes || len(primary) > maxClipboardBytes {
		return fmt.Errorf("clipboard exceeds 8 MiB")
	}
	var header [8]byte
	binary.LittleEndian.PutUint32(header[:4], uint32(len(text)))
	binary.LittleEndian.PutUint32(header[4:], uint32(len(primary)))
	inputR, inputW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer inputR.Close()
	defer inputW.Close()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readyR.Close()
	defer readyW.Close()
	command := exec.Command(executable, "--clipboard-owner")
	command.Stdin = inputR
	command.ExtraFiles = []*os.File{readyW}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	inputR.Close()
	readyW.Close()
	go command.Wait()
	written := make(chan error, 1)
	go func() {
		err := writeAll(inputW, header[:])
		if err == nil {
			err = writeAll(inputW, []byte(text))
		}
		if err == nil {
			err = writeAll(inputW, []byte(primary))
		}
		inputW.Close()
		written <- err
	}()
	ready := make(chan bool, 1)
	go func() { var ack [1]byte; n, _ := readyR.Read(ack[:]); ready <- n == 1 && ack[0] == '1' }()
	select {
	case ok := <-ready:
		if !ok {
			command.Process.Kill()
			return fmt.Errorf("clipboard handoff failed")
		}
	case <-time.After(3 * time.Second):
		command.Process.Kill()
		return fmt.Errorf("clipboard handoff timed out")
	}
	return <-written
}
func clipboardOwner() int {
	text, primary, err := readClipboardPayload(os.Stdin)
	if err != nil {
		return 1
	}
	c, err := newXClipboard(false)
	if err != nil {
		return 1
	}
	defer c.Close()
	if text != "" {
		if c.Set(text, false) != nil {
			return 1
		}
	}
	if primary != "" {
		if c.Set(primary, true) != nil {
			return 1
		}
	}
	ack := os.NewFile(3, "clipboard-ready")
	if ack != nil {
		ack.Write([]byte("1"))
		ack.Close()
	}
	select {
	case <-c.done:
	case <-time.After(24 * time.Hour):
	}
	return 0
}

func readClipboardPayload(reader io.Reader) (string, string, error) {
	var header [8]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return "", "", err
	}
	a, z := int(binary.LittleEndian.Uint32(header[:4])), int(binary.LittleEndian.Uint32(header[4:]))
	if a > maxClipboardBytes || z > maxClipboardBytes {
		return "", "", fmt.Errorf("clipboard exceeds 8 MiB")
	}
	data := make([]byte, a+z)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", "", err
	}
	if !utf8.Valid(data[:a]) || !utf8.Valid(data[a:]) {
		return "", "", fmt.Errorf("invalid UTF-8 clipboard")
	}
	return string(data[:a]), string(data[a:]), nil
}
func (c *xClipboard) decodeText(target xproto.Atom, format byte, data []byte) (string, error) {
	if format != 8 {
		return "", fmt.Errorf("clipboard text is not 8-bit")
	}
	if target == c.atoms["STRING"] {
		text := make([]rune, len(data))
		for i, value := range data {
			text[i] = rune(value)
		}
		return string(text), nil
	}
	if target != c.atoms["UTF8_STRING"] || !utf8.Valid(data) {
		return "", fmt.Errorf("clipboard did not provide valid UTF-8")
	}
	return string(data), nil
}
func latin1Text(text string) string {
	var out []byte
	for _, r := range text {
		if r > 255 {
			out = append(out, '?')
		} else {
			out = append(out, byte(r))
		}
	}
	return string(out)
}
