package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

const syntaxSliceBudget = 20 * time.Millisecond
const syntaxViewportMargin = 40
const maxSyntaxDocuments = 8
const maxSyntaxCheckpoints = 8192
const maxCachedSyntaxTokens = 262144

// Worker cache keys must not retain a closed Buffer (and its undo/journal).
type syntaxIdentity struct{ marker byte }

type syntaxChange struct {
	serial uint64
	dirty  int
	edits  []patch
	reset  bool
}
type syntaxCheckpoint struct {
	at    int
	stack []string
	tex   texSyntaxState
}
type syntaxDocument struct {
	path                         string
	serial                       uint64
	tokens                       []syntaxToken
	points                       []syntaxCheckpoint
	dirty, validUntil, damageEnd int
	adapter                      *checkpointLexer
	lexer                        chroma.Lexer
	tex                          bool
	used                         uint64
}

// Chroma's public LexerState contains the lexical stack but its normal
// Tokenise API does not expose it. A one-shot bootstrap rule captures that
// state and resumes a saved stack without modifying the original root rules.
// No unsafe/reflection, dependency patches or mutation of Chroma's registry.
type checkpointLexer struct {
	lexer *chroma.RegexLexer
	state *chroma.LexerState
	text  []rune
	point syntaxCheckpoint
}

func newCheckpointLexer(source *chroma.RegexLexer) (*checkpointLexer, error) {
	rules, err := source.Rules()
	if err != nil {
		return nil, err
	}
	adapter := &checkpointLexer{}
	rules = rules.Clone()
	bootstrap := chroma.Rule{Pattern: "", Type: chroma.Text, Mutator: chroma.MutatorFunc(func(state *chroma.LexerState) error {
		if adapter.state == nil {
			adapter.state = state
			state.Text = adapter.text
			state.Pos = adapter.point.at
			state.Stack = append([]string(nil), adapter.point.stack...)
		}
		return nil
	})}
	rules["__govim_resume"] = []chroma.Rule{bootstrap}
	config := *source.Config()
	adapter.lexer, err = chroma.NewLexer(&config, func() chroma.Rules { return rules })
	if err != nil {
		return nil, err
	}
	adapter.lexer.SetRegistry(lexers.GlobalLexerRegistry)
	return adapter, nil
}
func (a *checkpointLexer) start(text []rune, point syntaxCheckpoint) (chroma.Iterator, error) {
	a.text, a.point, a.state = text, point, nil
	options := &chroma.TokeniseOptions{State: "__govim_resume", EnsureLF: false}
	iterator, err := a.lexer.Tokenise(options, "\n")
	if err != nil {
		return nil, err
	}
	// Consume only the empty bootstrap token. Original input is untouched.
	if token := iterator(); token.Type != chroma.Text || token.Value != "" || a.state == nil {
		return nil, fmt.Errorf("syntax bootstrap failed")
	}
	// Chroma's error recovery resets to options.State on an unmatched newline.
	options.State = "root"
	return iterator, nil
}
func (a *checkpointLexer) checkpoint(at int) (syntaxCheckpoint, bool) {
	state := a.state
	if state == nil || state.Pos != at || len(state.MutatorContext) != 0 {
		return syntaxCheckpoint{}, false
	}
	return syntaxCheckpoint{at: at, stack: append([]string(nil), state.Stack...)}, true
}

// Edits are journaled/applied before this presentation-only invalidation.
// Preserve colors (and shift offsets) until their replacement is available.
func (b *Buffer) invalidateSyntax(record journalRecord) {
	cache := &b.syntax
	cache.serial++
	dirty := 0
	if len(record.Edits) > 0 {
		at := record.Edits[0].At
		for _, p := range record.Edits {
			at = min(at, p.At)
		}
		dirty = b.start(max(0, b.row(at)-1))
		if len(record.Edits) > 1 && !disjointDescending(record.Edits) {
			dirty = 0
		}
	}
	change := syntaxChange{serial: cache.serial, dirty: dirty, reset: len(record.Edits) > 64}
	if !change.reset {
		change.edits = append([]patch(nil), record.Edits...)
		cache.tokens = shiftSyntaxTokens(cache.tokens, record.Edits)
	} else {
		cache.tokens = nil
	}
	cache.changes = append(cache.changes, change)
	if len(cache.changes) > 128 {
		cache.changes = []syntaxChange{{serial: cache.serial, reset: true}}
	}
	cache.ready = false
}
func shiftSyntaxTokens(tokens []syntaxToken, edits []patch) []syntaxToken {
	result := append([]syntaxToken(nil), tokens...)
	for _, p := range edits {
		out := result[:0]
		oldEnd := p.At + utf8.RuneCountInString(p.Old)
		newLen := utf8.RuneCountInString(p.New)
		delta := newLen - (oldEnd - p.At)
		for _, token := range result {
			if token.end <= p.At {
				out = append(out, token)
				continue
			}
			if token.start >= oldEnd {
				token.start += delta
			} else if token.start >= p.At {
				token.start = p.At
			}
			if token.end >= oldEnd {
				token.end += delta
			} else {
				token.end = p.At + newLen
			}
			if token.end > token.start {
				out = append(out, token)
			}
		}
		result = out
	}
	return result
}
func (d *syntaxDocument) apply(change syntaxChange) {
	if change.reset {
		d.tokens = nil
		d.points = nil
		d.dirty, d.validUntil, d.damageEnd = 0, 0, 0
		d.serial = change.serial
		return
	}
	d.tokens = shiftSyntaxTokens(d.tokens, change.edits)
	for _, p := range change.edits {
		oldEnd := p.At + utf8.RuneCountInString(p.Old)
		newEnd := p.At + utf8.RuneCountInString(p.New)
		d.dirty = adjusted(d.dirty, p)
		d.validUntil = adjusted(d.validUntil, p)
		d.damageEnd = max(adjusted(d.damageEnd, p), newEnd)
		out := d.points[:0]
		for _, point := range d.points {
			if point.at > p.At && point.at <= oldEnd {
				continue
			}
			if point.at > oldEnd {
				point.at += newEnd - oldEnd
			}
			out = append(out, point)
		}
		d.points = out
	}
	d.dirty = min(d.dirty, change.dirty)
	d.serial = change.serial
}
func newSyntaxCalculator() func(syntaxRequest, <-chan struct{}) syntaxResult {
	documents := make(map[*syntaxIdentity]*syntaxDocument)
	var clock uint64
	return func(request syntaxRequest, stop <-chan struct{}) (result syntaxResult) {
		result.syntaxRequest = request
		defer func() {
			if problem := recover(); problem != nil {
				delete(documents, request.identity)
				result.err = fmt.Errorf("incremental syntax: %v", problem)
				result.complete = true
			}
		}()
		text := request.runes
		if text == nil {
			text = []rune(request.text)
		}
		doc := documents[request.identity]
		selected := syntaxLexer(request.key.path, string(text[:min(len(text), 1024)]))
		name := func(lexer chroma.Lexer) string {
			if lexer == nil {
				return ""
			}
			return lexer.Config().Name
		}
		if doc == nil || doc.path != request.key.path || name(doc.lexer) != name(selected) {
			doc = &syntaxDocument{path: request.key.path}
			if len(documents) >= maxSyntaxDocuments {
				var oldest *syntaxIdentity
				age := ^uint64(0)
				for id, cached := range documents {
					if cached.used < age {
						oldest, age = id, cached.used
					}
				}
				delete(documents, oldest)
			}
			documents[request.identity] = doc
			doc.lexer = selected
			if doc.lexer != nil {
				result.lexer = doc.lexer.Config().Name
				doc.tex = strings.EqualFold(result.lexer, "TeX")
				if source, ok := doc.lexer.(*chroma.RegexLexer); ok && !doc.tex {
					var err error
					doc.adapter, err = newCheckpointLexer(source)
					if err != nil {
						result.err = err
						result.complete = true
						return
					}
				}
			}
		}
		clock++
		doc.used = clock
		if doc.adapter != nil {
			defer func() { doc.adapter.text = nil; doc.adapter.state = nil }()
		}
		if doc.lexer == nil {
			result.complete = true
			return
		}
		result.lexer = doc.lexer.Config().Name
		for _, change := range request.changes {
			if change.serial > doc.serial {
				doc.apply(change)
			}
		}
		if doc.serial != request.serial {
			doc.tokens = nil
			doc.points = nil
			doc.dirty, doc.validUntil, doc.damageEnd = 0, 0, 0
			doc.serial = request.serial
		}
		target := min(len(text), request.last)
		if request.last == 0 {
			target = len(text)
		}
		if doc.adapter == nil && !doc.tex {
			// Delegating/non-regex lexers do not expose restartable state. Keep
			// their conservative bounded fallback, without clearing old colors.
			if target > maxSyntaxBytes {
				result.complete = true
				return
			}
			request.text = string(text[:target])
			result = calculateSyntax(request, stop)
			result.complete = true
			return
		}
		deadline := time.Now().Add(syntaxSliceBudget)
		result.scanned, result.err = doc.calculate(text, target, stop, deadline)
		result.complete = doc.dirty >= target || result.err != nil
		// Keep memory bounded even after traversing a very large document.
		// Sparse checkpoints still preserve exact state, just at wider intervals.
		for len(doc.points) > maxSyntaxCheckpoints {
			points := doc.points[:0]
			for i, point := range doc.points {
				if i%2 == 0 || i == len(doc.points)-1 {
					points = append(points, point)
				}
			}
			doc.points = points
		}
		if len(doc.tokens) > maxCachedSyntaxTokens {
			delete(documents, request.identity)
			result.err = fmt.Errorf("syntax cache limit exceeded")
			result.complete = true
			return
		}
		// Return only viewport colors. Off-screen lexical state/checkpoints are
		// cached, but not marshaled and repainted as a whole-document result.
		first := max(0, request.first)
		last := max(target, request.last)
		for _, token := range doc.tokens {
			if token.end <= first {
				continue
			}
			if token.start >= last {
				break
			}
			result.tokens = append(result.tokens, token)
		}
		return
	}
}
func (d *syntaxDocument) calculate(text []rune, target int, stop <-chan struct{}, deadline time.Time) (int, error) {
	if d.dirty >= target {
		return 0, nil
	}
	point := syntaxCheckpoint{stack: []string{"root"}}
	for _, candidate := range d.points {
		if candidate.at > d.dirty {
			break
		}
		point = candidate
	}
	if d.tex {
		return d.calculateTex(text, target, point, stop, deadline)
	}
	iterator, err := d.adapter.start(text, point)
	if err != nil {
		return 0, err
	}
	pos, start := point.at, point.at
	var tokens []syntaxToken
	points := []syntaxCheckpoint{point}
	safe := point
	styles := make(map[chroma.TokenType]string)
	plain := syntaxForeground(chroma.Text)
	for count := 0; count < maxSyntaxTokens; count++ {
		select {
		case <-stop:
			return pos - start, fmt.Errorf("syntax cancelled")
		default:
		}
		token := iterator()
		if token == chroma.EOF {
			if pos != len(text) {
				return pos - start, fmt.Errorf("lexer omitted text")
			}
			d.replaceSyntax(start, len(text), tokens, points)
			d.dirty, d.validUntil = len(text), len(text)
			return pos - start, nil
		}
		length := utf8.RuneCountInString(token.Value)
		end := pos + length
		if end > len(text) || string(text[pos:end]) != token.Value {
			return pos - start, fmt.Errorf("lexer changed text")
		}
		style, ok := styles[token.Type]
		if !ok {
			style = syntaxForeground(token.Type)
			styles[token.Type] = style
		}
		if length > 0 && style != plain {
			tokens = appendSyntaxToken(tokens, syntaxToken{pos, end, style})
		}
		pos = end
		if next, ok := d.adapter.checkpoint(pos); ok {
			safe = next
			if pos == len(text) || pos > 0 && text[pos-1] == '\n' || pos >= target || time.Now().After(deadline) {
				points = append(points, next)
				converged := false
				if pos > start && pos > d.damageEnd {
					index := sort.Search(len(d.points), func(i int) bool { return d.points[i].at >= pos })
					converged = index < len(d.points) && d.points[index].at == pos && equalSyntaxState(d.points[index], next)
				}
				if converged {
					d.replaceSyntax(start, pos, tokens, points)
					d.dirty = max(pos, d.validUntil)
					return pos - start, nil
				}
				if pos >= target || time.Now().After(deadline) {
					d.replaceSyntax(start, pos, tokens, points)
					d.stopAt(pos)
					return pos - start, nil
				}
			}
		}
	}
	if safe.at > start {
		for len(tokens) > 0 && tokens[len(tokens)-1].start >= safe.at {
			tokens = tokens[:len(tokens)-1]
		}
		if len(tokens) > 0 {
			tokens[len(tokens)-1].end = min(tokens[len(tokens)-1].end, safe.at)
		}
		points = append(points, safe)
		d.replaceSyntax(start, safe.at, tokens, points)
		d.stopAt(safe.at)
	} else {
		return pos - start, fmt.Errorf("lexer has no bounded restart point")
	}
	return pos - start, nil
}

// A non-converged slice has not proved the old tail's state valid. Keep its
// colors as display hints, but don't later converge against a mixed revision.
func (d *syntaxDocument) stopAt(pos int) {
	d.dirty, d.validUntil = pos, pos
	end := sort.Search(len(d.points), func(i int) bool { return d.points[i].at > pos })
	d.points = d.points[:end]
}
func appendSyntaxToken(tokens []syntaxToken, token syntaxToken) []syntaxToken {
	if token.end <= token.start {
		return tokens
	}
	if len(tokens) > 0 && tokens[len(tokens)-1].end == token.start && tokens[len(tokens)-1].style == token.style {
		tokens[len(tokens)-1].end = token.end
		return tokens
	}
	return append(tokens, token)
}
func equalSyntaxState(a, b syntaxCheckpoint) bool {
	if a.tex != b.tex || len(a.stack) != len(b.stack) {
		return false
	}
	for i, state := range a.stack {
		if state != b.stack[i] {
			return false
		}
	}
	return true
}
func (d *syntaxDocument) replaceSyntax(first, last int, tokens []syntaxToken, points []syntaxCheckpoint) {
	var merged []syntaxToken
	for _, token := range d.tokens {
		if token.start >= first {
			break
		}
		token.end = min(token.end, first)
		merged = appendSyntaxToken(merged, token)
	}
	for _, token := range tokens {
		merged = appendSyntaxToken(merged, token)
	}
	for _, token := range d.tokens {
		if token.end <= last {
			continue
		}
		token.start = max(token.start, last)
		merged = appendSyntaxToken(merged, token)
	}
	d.tokens = merged
	var checkpoints []syntaxCheckpoint
	for _, point := range d.points {
		if point.at < first {
			checkpoints = append(checkpoints, point)
		}
	}
	checkpoints = append(checkpoints, points...)
	for _, point := range d.points {
		if point.at > last {
			checkpoints = append(checkpoints, point)
		}
	}
	d.points = checkpoints
}
