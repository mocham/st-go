// Package aiharness provides explicit-context paragraph generation and a
// project-local, versioned UUID-to-insertion dictionary. It owns no terminal UI.
package aiharness

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const Slot = "<++>"
const MaxInput = 4 << 20
const MaxParagraph = 512 << 10

type Project struct {
	Database    string
	ContextFile string
	Server      string
	ServerSSH   string
	Model       string
	Variant     string
}

func projectDirective(key string) bool {
	return key == "%project" || key == "%context" || key == "%server" || key == "%server-ssh" || key == "%model" || key == "%variant"
}

// Resolve reads explicit, whole-line directives from the current buffer.
// Relative paths belong to the document, never the editor's working directory.
func Resolve(document, text string) (Project, error) {
	if document == "" {
		return Project{}, fmt.Errorf("AI insertion requires a named document")
	}
	document, err := filepath.Abs(document)
	if err != nil {
		return Project{}, err
	}
	p := Project{Database: document + ".db"}
	seen := make(map[string]bool)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok || !projectDirective(key) {
			continue
		}
		value = strings.TrimSpace(value)
		if seen[key] || value == "" {
			return Project{}, fmt.Errorf("empty or duplicate %s directive", key)
		}
		seen[key] = true
		switch key {
		case "%server":
			p.Server = value
			continue
		case "%server-ssh":
			p.ServerSSH = value
			continue
		case "%model":
			p.Model = value
			continue
		case "%variant":
			p.Variant = value
			continue
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(filepath.Dir(document), value)
		}
		value = filepath.Clean(value)
		if key == "%project" {
			p.Database = value
		} else {
			p.ContextFile = value
		}
	}
	if p.Database == document || p.Database == p.ContextFile {
		return Project{}, fmt.Errorf("project database must differ from the document and context file")
	}
	return p, nil
}

func ReadContext(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("context: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxInput+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxInput || !utf8.Valid(data) {
		return "", fmt.Errorf("context must be UTF-8 and at most %d bytes", MaxInput)
	}
	return string(data), nil
}

func UUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6], b[8] = b[6]&15|64, b[8]&63|128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func ValidUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func BlockText(id, paragraph string) string { return "{{{%" + id + "\n" + paragraph + "}}}" }

// Block offsets are rune offsets, matching the editor's cursor and journal.
type Block struct {
	UUID                       string
	Start, Content, Close, End int
}

func Blocks(text string) []Block {
	runes := []rune(text)
	var blocks []Block
	for at := 0; at+41 <= len(runes); at++ {
		if string(runes[at:at+4]) != "{{{%" {
			continue
		}
		id := string(runes[at+4 : at+40])
		if !ValidUUID(id) || runes[at+40] != '\n' {
			continue
		}
		depth := 0
		for end := at + 41; end < len(runes); end++ {
			if runes[end] == '\\' && end+1 < len(runes) {
				end++
				continue
			}
			if depth == 0 && end+3 <= len(runes) && string(runes[end:end+3]) == "}}}" {
				blocks = append(blocks, Block{id, at, at + 41, end, end + 3})
				at = end + 2
				break
			}
			if runes[end] == '{' {
				depth++
			}
			if runes[end] == '}' {
				depth--
			}
			if depth < 0 {
				break
			}
		}
	}
	return blocks
}

func FindBlock(text, id string) (Block, error) {
	var found Block
	count := 0
	for _, b := range Blocks(text) {
		if b.UUID == id {
			found = b
			count++
		}
	}
	if count != 1 || strings.Count(text, "{{{%"+id+"\n") != 1 {
		return Block{}, fmt.Errorf("AI block missing, malformed, or duplicated")
	}
	return found, nil
}

// ProjectText removes AI bookkeeping for document preview while retaining line
// breaks, so source navigation remains aligned. Saved source is never rewritten.
func ProjectText(text string) string {
	runes := []rune(text)
	var out strings.Builder
	at := 0
	for _, b := range Blocks(text) {
		out.WriteString(string(runes[at:b.Start]))
		out.WriteByte('\n') // UUID header's source line
		body := string(runes[b.Content:b.Close])
		if body != Slot {
			out.WriteString(body)
		}
		at = b.End
	}
	out.WriteString(string(runes[at:]))
	lines := strings.Split(out.String(), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		key, _, ok := strings.Cut(line, "=")
		if ok && projectDirective(key) {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}
