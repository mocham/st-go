package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"

	"st-go/limits"
)

type previewCursor struct {
	block, line, step int
}

func (c previewCursor) key(source *sourceSnapshot) [sha256.Size]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("%x/%d/%d/%d", source.hash, c.block, c.line, c.step)))
}

func (c *previewCursor) navigate(kind string, value int) {
	switch kind {
	case "seek":
		c.line, c.step = value, 0
	case "scroll":
		c.step = max(-limits.MaxBytes, min(c.step+value, limits.MaxBytes))
	}
}

// Region event frames are removed before relaying input to Vim. All other
// bytes, including fragmented ordinary escape keys, are preserved verbatim.
type navigationInput struct {
	pending []byte
	since   time.Time
}

const navigationPrefix = "\x1bPregion secondary event "
const navigationWait = 25 * time.Millisecond

func (p *navigationInput) feed(data []byte, now time.Time, event func(string, int)) []byte {
	p.pending = append(p.pending, data...)
	var output []byte
	for len(p.pending) > 0 {
		start := bytes.Index(p.pending, []byte(navigationPrefix))
		if start < 0 {
			keep := min(len(p.pending), len(navigationPrefix)-1)
			for keep > 0 && !bytes.Equal(p.pending[len(p.pending)-keep:], []byte(navigationPrefix[:keep])) {
				keep--
			}
			output = append(output, p.pending[:len(p.pending)-keep]...)
			p.pending = p.pending[len(p.pending)-keep:]
			break
		}
		output = append(output, p.pending[:start]...)
		p.pending = p.pending[start:]
		end := bytes.Index(p.pending, []byte("\x1b\\"))
		if end < 0 {
			if len(p.pending) < 128 {
				break
			}
			output = append(output, p.pending[0])
			p.pending = p.pending[1:]
			continue
		}
		fields := strings.Fields(string(p.pending[len(navigationPrefix):end]))
		valid := false
		if len(fields) == 2 && (fields[0] == "seek" || fields[0] == "scroll") {
			value, err := strconv.Atoi(fields[1])
			if err == nil && value >= -limits.MaxBytes && value <= limits.MaxBytes && (fields[0] != "seek" || value > 0) {
				event(fields[0], value)
				valid = true
			}
		}
		if !valid {
			output = append(output, p.pending[:end+2]...)
		}
		p.pending = p.pending[end+2:]
	}
	if len(p.pending) > 0 {
		p.since = now
	} else {
		p.pending = nil
	}
	return output
}

func (p *navigationInput) expire(now time.Time) []byte {
	if len(p.pending) == 0 || now.Sub(p.since) < navigationWait {
		return nil
	}
	data := p.pending
	p.pending = nil
	return data
}
