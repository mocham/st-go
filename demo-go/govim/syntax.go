package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// Highlighting is optional presentation work. One worker processes immutable
// snapshots, never buffers or journals. Large/slow/broken lexers fall back to
// plain text; neither their output nor a panic can change document contents.
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
	key, requested syntaxKey
	ready, waiting bool
	tokens         []syntaxToken
}

type syntaxRequest struct {
	buffer *Buffer // identity only: the worker must not dereference it
	key    syntaxKey
	text   string
}

type syntaxResult struct {
	syntaxRequest
	tokens []syntaxToken
	lexer  string
	err    error
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
				if cache.waiting && cache.requested == result.key {
					cache.waiting = false
					if result.key == (syntaxKey{b.version, b.meta.Path}) {
						cache.key, cache.ready = result.key, true
						cache.tokens = result.tokens
						if result.err != nil {
							cache.tokens = nil
						}
					}
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

func (e *Editor) syntaxTokens(b *Buffer) []syntaxToken {
	key := syntaxKey{b.version, b.meta.Path}
	cache := &b.syntax
	if cache.ready && cache.key == key {
		return cache.tokens
	}
	// Never paint stale offsets while an edited/renamed document is re-lexed.
	cache.tokens = nil
	cache.ready = false
	if b.byteSize > maxSyntaxBytes || len(b.text) > maxSyntaxBytes {
		cache.key, cache.ready = key, true
		return nil
	}
	if cache.waiting {
		return nil
	}
	if e.syntax == nil {
		e.syntax = newSyntaxHighlighter(calculateSyntax)
	}
	if e.syntax.closed {
		return nil
	}
	// Do not copy source text if the single pending-work slot is occupied.
	if len(e.syntax.requests) == cap(e.syntax.requests) {
		return nil
	}
	request := syntaxRequest{buffer: b, key: key, text: string(b.text)}
	if len(request.text) > maxSyntaxBytes {
		cache.key, cache.ready = key, true
		return nil
	}
	select {
	case e.syntax.requests <- request:
		cache.requested, cache.waiting = key, true
	default:
	}
	return nil
}

// Chroma handles token classification and style inheritance. This small style
// definition preserves govim's warm desert palette rather than loading any Vim
// runtime files or executing configuration from the document being edited.
var desertSyntaxStyle = chroma.MustNewStyle("govim-desert", chroma.StyleEntries{
	chroma.Text:                "#ffd7af",
	chroma.Comment:             "#87af87",
	chroma.CommentPreproc:      "#d7af87",
	chroma.Keyword:             "#87afff",
	chroma.KeywordType:         "#bdb76b",
	chroma.NameBuiltin:         "#87afff",
	chroma.NameClass:           "#bdb76b",
	chroma.NameException:       "#ffaf5f",
	chroma.NameFunction:        "#ffd75f",
	chroma.NameDecorator:       "#87d7ff",
	chroma.NameTag:             "#87d7ff",
	chroma.NameAttribute:       "#bdb76b",
	chroma.LiteralString:       "#d7af87",
	chroma.LiteralStringEscape: "#ffaf5f",
	chroma.LiteralNumber:       "#ffaf5f",
	chroma.GenericHeading:      "bold #ffaf5f",
	chroma.GenericSubheading:   "bold #ffaf5f",
	chroma.GenericStrong:       "bold",
	chroma.GenericEmph:         "italic",
	chroma.GenericUnderline:    "underline",
	chroma.GenericInserted:     "#87af87",
	chroma.GenericDeleted:      "#ff8787",
})

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
		if lexer := lexers.Match(filepath.Base(path)); lexer != nil {
			return lexer
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
