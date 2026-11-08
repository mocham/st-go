package main

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"st-go/limits"
)

type previewNavigation struct {
	line, step int
}

func (c *previewNavigation) navigate(kind string, value int) {
	switch kind {
	case "seek":
		c.line, c.step = value, 0
	case "scroll":
		c.step = max(-limits.MaxBytes, min(c.step+value, limits.MaxBytes))
	}
}

// Explicit Vimscript seeks remain approximate source positions. Scrolling is
// local to st-go and never changes the saved-source render key.
func (c previewNavigation) command(source *sourceSnapshot) string {
	var commands []string
	if c.line > 0 {
		position := source.previewPosition(source.expandedLine(c.line))
		commands = append(commands, fmt.Sprintf("region secondary position %d", position))
	}
	if c.step != 0 {
		commands = append(commands, fmt.Sprintf("region secondary scroll %d", c.step))
	}
	return strings.Join(commands, "; ")
}

// previewPosition takes an expanded source line, so markers inside includes
// and explicit seeks from root-file lines use the same approximate mapping.
func (s *sourceSnapshot) previewPosition(line int) int {
	first, last := 1, 1+strings.Count(s.text, "\n")
	if begin := strings.Index(s.text, `\begin{document}`); begin >= 0 {
		first = 1 + strings.Count(s.text[:begin], "\n")
	}
	if end := strings.LastIndex(s.text, `\end{document}`); end >= 0 {
		last = 1 + strings.Count(s.text[:end], "\n")
	}
	line = max(first, min(line, last))
	return int(int64(line-first) * 1000000 / int64(max(1, last-first)))
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
