package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// Highlighting is optional presentation work. One worker processes immutable
// snapshots, never buffers or journals. Viewport-demand/checkpoint calculation
// lives in syntax_incremental.go; the bounded whole-prefix helper below is for
// non-restartable lexers and full-lex regression comparisons. Neither a lexer
// result nor a panic can change document contents.
const maxSyntaxBytes = 1 << 20
const maxSyntaxTokens = 65536
const syntaxBudget = time.Second

type syntaxToken struct {
	start, end int    // absolute rune offsets, not UTF-8 byte offsets or screen cells
	style      string // SGR foreground/attributes only; the editor owns backgrounds
}

type syntaxKey struct {
	version uint64
	path    string
}

type syntaxCache struct {
	key, requested  syntaxKey
	ready, waiting  bool
	tokens          []syntaxToken
	serial          uint64
	changes         []syntaxChange
	requestedSerial uint64
	first, last     int
	scanned         int
	identity        *syntaxIdentity
}

type syntaxRequest struct {
	buffer      *Buffer // identity only: the worker must not dereference it
	identity    *syntaxIdentity
	key         syntaxKey
	text        string
	runes       []rune // immutable buffer snapshot; no whole-buffer UTF-8 copy
	changes     []syntaxChange
	serial      uint64
	first, last int
}

type syntaxResult struct {
	syntaxRequest
	tokens   []syntaxToken
	lexer    string
	err      error
	complete bool
	scanned  int
}

type syntaxHighlighter struct {
	requests   chan syntaxRequest
	results    chan syntaxResult
	stop, done chan struct{}
	closed     bool // owned by the editor event loop
}

func newSyntaxHighlighter(calculate func(syntaxRequest, <-chan struct{}) syntaxResult) *syntaxHighlighter {
	h := &syntaxHighlighter{requests: make(chan syntaxRequest, 1), results: make(chan syntaxResult, 1), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(h.done)
		for {
			select {
			case <-h.stop:
				return
			case request := <-h.requests:
				result := calculate(request, h.stop)
				// Do not retain the source snapshot in the result/cache.
				result.text = ""
				result.runes = nil
				result.changes = nil
				select {
				case h.results <- result:
				case <-h.stop:
					return
				}
			}
		}
	}()
	return h
}

func (h *syntaxHighlighter) close() {
	if h != nil && !h.closed {
		h.closed = true
		close(h.stop)
	}
}

func (e *Editor) pollSyntax() {
	if e.syntax == nil {
		return
	}
	for {
		select {
		case result := <-e.syntax.results:
			for _, b := range e.buffers {
				if b != result.buffer {
					continue
				}
				cache := &b.syntax
				if cache.waiting && cache.requested == result.key && cache.requestedSerial == result.serial {
					cache.waiting = false
					if result.key == (syntaxKey{b.version, b.meta.Path}) && result.serial == cache.serial {
						cache.key, cache.ready = result.key, result.complete || result.err != nil
						if result.err == nil {
							cache.tokens = result.tokens
						}
						cache.scanned = result.scanned
						cache.first, cache.last = result.first, result.last
					}
					kept := cache.changes[:0]
					for _, change := range cache.changes {
						if change.serial > result.serial {
							kept = append(kept, change)
						}
					}
					cache.changes = kept
				}
				break
			}
			// Also redraw for stale/deleted-buffer results: queued work has made
			// progress, so visible buffers can submit a fresh snapshot now.
			e.dirty = true
		default:
			return
		}
	}
}

func (e *Editor) syntaxTokens(b *Buffer, demand ...int) []syntaxToken {
	key := syntaxKey{b.version, b.meta.Path}
	cache := &b.syntax
	first, last := 0, len(b.text)
	if len(demand) == 2 {
		first, last = demand[0], demand[1]
	}
	if cache.ready && cache.key == key && cache.first <= first && cache.last >= last {
		return cache.tokens
	}
	// Plain text has no asynchronous color pass to wait for. Recheck unnamed
	// prose after an edit so adding a supported shebang can still enable syntax.
	if cache.identity == nil && syntaxLexer(key.path, string(b.text[:min(len(b.text), 1024)])) == nil {
		cache.key, cache.ready = key, true
		cache.first, cache.last = first, last
		cache.tokens = nil
		cache.changes = nil
		return nil
	}
	if cache.key.path != key.path {
		cache.tokens = nil
	}
	cache.ready = false
	if cache.waiting {
		return cache.tokens
	}
	if e.syntax == nil {
		e.syntax = newSyntaxHighlighter(newSyntaxCalculator())
	}
	if e.syntax.closed {
		return cache.tokens
	}
	// Do not copy source text if the single pending-work slot is occupied.
	if len(e.syntax.requests) == cap(e.syntax.requests) {
		return cache.tokens
	}
	if cache.identity == nil {
		cache.identity = &syntaxIdentity{}
	}
	request := syntaxRequest{buffer: b, identity: cache.identity, key: key, runes: b.text, serial: cache.serial, changes: append([]syntaxChange(nil), cache.changes...), first: first, last: last}
	select {
	case e.syntax.requests <- request:
		cache.requested, cache.waiting = key, true
		cache.requestedSerial = cache.serial
	default:
	}
	return cache.tokens
}

// Embedded data, not executable Vimscript or files read from the edited tree.
// See syntax/README.md for the Vim91 desert/TeX semantic-group mapping.
//
//go:embed syntax/desert.json
var desertSyntaxJSON []byte

var desertSyntaxStyle = loadSyntaxStyle(desertSyntaxJSON)

func loadSyntaxStyle(data []byte) *chroma.Style {
	var names map[string]string
	if err := json.Unmarshal(data, &names); err != nil {
		panic(err)
	}
	entries := chroma.StyleEntries{}
	for name, value := range names {
		kind, err := chroma.TokenTypeString(name)
		if err != nil {
			panic(err)
		}
		entries[kind] = value
	}
	return chroma.MustNewStyle("govim-desert", entries)
}

func syntaxForeground(kind chroma.TokenType) string {
	entry := desertSyntaxStyle.Get(kind)
	if !entry.Colour.IsSet() {
		entry.Colour = desertSyntaxStyle.Get(chroma.Text).Colour
	}
	style := fmt.Sprintf("38;2;%d;%d;%d", entry.Colour.Red(), entry.Colour.Green(), entry.Colour.Blue())
	if entry.Bold == chroma.Yes {
		style += ";1"
	}
	if entry.Italic == chroma.Yes {
		style += ";3"
	}
	if entry.Underline == chroma.Yes {
		style += ";4"
	}
	return style
}

func syntaxCellStyle(foreground string, currentLine bool) string {
	background := 236
	if currentLine {
		background = 237
	}
	return fmt.Sprintf("\x1b[0;%s;48;5;%dm", foreground, background)
}

func syntaxLexer(path, text string) chroma.Lexer {
	if path != "" {
		name := filepath.Base(path)
		if lexer := lexers.Match(name); lexer != nil {
			return lexer
		}
		// Chroma covers extensions, Dockerfiles and most rc files. A few
		// standard extensionless shell startup files need explicit aliases.
		switch name {
		case ".profile", "profile", ".zprofile", ".zlogin", ".zlogout", ".zshenv", ".kshrc", ".ashrc", ".mkshrc":
			return lexers.Get("bash")
		}
	}
	// Filename matching covers normal files; content analysis is restricted to
	// short shebangs so unknown prose isn't guessed as an unrelated language.
	if strings.HasPrefix(text, "#!") {
		line, _, _ := strings.Cut(text, "\n")
		if len(line) > 1024 {
			return nil
		}
		for _, word := range strings.Fields(line[2:]) {
			name := filepath.Base(word)
			switch {
			case strings.HasPrefix(name, "python"):
				return lexers.Get("python")
			case name == "sh" || name == "bash" || name == "zsh" || name == "dash":
				return lexers.Get("bash")
			case name == "node" || name == "nodejs":
				return lexers.Get("javascript")
			case name == "ruby" || name == "perl" || name == "fish" || name == "lua":
				return lexers.Get(name)
			}
		}
	}
	return nil
}

func calculateSyntax(request syntaxRequest, stop <-chan struct{}) (result syntaxResult) {
	result.syntaxRequest = request
	defer func() {
		if problem := recover(); problem != nil {
			result.tokens = nil
			result.err = fmt.Errorf("syntax highlighting failed: %v", problem)
		}
	}()
	if len(request.text) > maxSyntaxBytes {
		result.err = fmt.Errorf("syntax input exceeds size limit")
		return
	}
	lexer := syntaxLexer(request.key.path, request.text)
	if lexer == nil {
		return
	}
	result.lexer = lexer.Config().Name
	result.tokens, result.err = lexSyntax(lexer, request.text, stop, time.Now().Add(syntaxBudget))
	if result.err != nil {
		result.tokens = nil
	}
	return
}

func lexSyntax(lexer chroma.Lexer, text string, stop <-chan struct{}, deadline time.Time) (tokens []syntaxToken, err error) {
	defer func() {
		if problem := recover(); problem != nil {
			tokens = nil
			err = fmt.Errorf("syntax lexer failed: %v", problem)
		}
	}()
	if err := syntaxInterrupted(stop, deadline); err != nil {
		return nil, err
	}
	// Preserve mixed CR/LF and exact text offsets. Some lexers synthesize a
	// final newline; it is checked/clipped below, never applied to the buffer.
	iterator, err := lexer.Tokenise(&chroma.TokeniseOptions{State: "root", EnsureLF: false}, text)
	if err != nil {
		return nil, err
	}
	plain := syntaxForeground(chroma.Text)
	styles := make(map[chroma.TokenType]string)
	byteOffset, runeOffset := 0, 0
	for count := 0; ; count++ {
		if count >= maxSyntaxTokens {
			return nil, fmt.Errorf("syntax token limit exceeded")
		}
		if err := syntaxInterrupted(stop, deadline); err != nil {
			return nil, err
		}
		token := iterator()
		if token == chroma.EOF {
			break
		}
		value := token.Value
		if len(value) > len(text)-byteOffset {
			if len(value) != len(text)-byteOffset+1 || !strings.HasSuffix(value, "\n") {
				return nil, fmt.Errorf("lexer changed document text")
			}
			value = value[:len(value)-1]
		}
		end := byteOffset + len(value)
		if text[byteOffset:end] != value {
			return nil, fmt.Errorf("lexer changed document text")
		}
		length := utf8.RuneCountInString(value)
		style, ok := styles[token.Type]
		if !ok {
			style = syntaxForeground(token.Type)
			styles[token.Type] = style
		}
		if length > 0 && style != plain {
			if len(tokens) > 0 && tokens[len(tokens)-1].end == runeOffset && tokens[len(tokens)-1].style == style {
				tokens[len(tokens)-1].end += length
			} else {
				tokens = append(tokens, syntaxToken{runeOffset, runeOffset + length, style})
			}
		}
		byteOffset, runeOffset = end, runeOffset+length
	}
	if byteOffset != len(text) {
		return nil, fmt.Errorf("lexer omitted document text")
	}
	return tokens, nil
}

func syntaxInterrupted(stop <-chan struct{}, deadline time.Time) error {
	select {
	case <-stop:
		return fmt.Errorf("syntax highlighting cancelled")
	default:
	}
	if time.Now().After(deadline) {
		return fmt.Errorf("syntax highlighting budget exceeded")
	}
	return nil
}
