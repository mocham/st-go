package googlepinyin

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type tokenKind uint8

const (
	tokenFull tokenKind = iota
	tokenPrefix
	tokenInitial
)

type spellingToken struct {
	text string
	kind tokenKind
}

type rankedEntry struct {
	entry entry
	order int
}

// Lookup returns at most limit candidates. Whole-prompt lemmas are followed by
// progressively shorter prefix lemmas, mirroring Google Pinyin's frontend list
// without its mutable native search matrix.
func (decoder *Decoder) Lookup(prompt string, limit int) ([]string, error) {
	if decoder == nil || len(decoder.entries) == 0 {
		return nil, fmt.Errorf("Pinyin decoder is not initialized")
	}
	if err := validatePrompt(prompt); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxCandidates {
		return nil, fmt.Errorf("Pinyin candidate limit must be 1..%d", MaxCandidates)
	}
	tokens := decoder.tokenize(prompt)
	if len(tokens) == 0 || len(tokens) > MaxPrompt {
		return nil, nil
	}
	groups := make([][]rankedEntry, len(tokens)+1)
	order := 0
	maximum := min(len(tokens), len(decoder.entries)-1)
	for length := 1; length <= maximum; length++ {
		for _, candidate := range decoder.entries[length] {
			if matchSpellings(candidate.syllables, tokens[:length]) {
				groups[length] = append(groups[length], rankedEntry{candidate, order})
				order++
			}
		}
	}
	for length := 1; length <= maximum; length++ {
		sort.SliceStable(groups[length], func(i, j int) bool {
			if groups[length][i].entry.frequency != groups[length][j].entry.frequency {
				return groups[length][i].entry.frequency > groups[length][j].entry.frequency
			}
			if groups[length][i].entry.text != groups[length][j].entry.text {
				return groups[length][i].entry.text < groups[length][j].entry.text
			}
			return groups[length][i].order < groups[length][j].order
		})
	}
	result := make([]string, 0, min(limit, 256))
	seen := make(map[string]struct{}, min(limit, 256))
	appendCandidate := func(text string) bool {
		if text == "" {
			return false
		}
		if _, exists := seen[text]; exists {
			return false
		}
		seen[text] = struct{}{}
		result = append(result, text)
		return len(result) == limit
	}
	if len(groups[len(tokens)]) == 0 {
		if sentence := decoder.compose(tokens); sentence != "" && appendCandidate(sentence) {
			return result, nil
		}
	}
	for length := maximum; length >= 1; length-- {
		for _, candidate := range groups[length] {
			if appendCandidate(candidate.entry.text) {
				return result, nil
			}
		}
	}
	return result, nil
}

func validatePrompt(prompt string) error {
	if len(prompt) < 1 || len(prompt) > MaxPrompt {
		return fmt.Errorf("Pinyin prompt must contain 1..%d bytes", MaxPrompt)
	}
	previousApostrophe := true
	for _, character := range []byte(prompt) {
		if character == '\'' {
			if previousApostrophe {
				return fmt.Errorf("invalid Pinyin apostrophe")
			}
			previousApostrophe = true
			continue
		}
		if character < 'a' || character > 'z' {
			return fmt.Errorf("Pinyin accepts lowercase letters and apostrophes only")
		}
		previousApostrophe = false
	}
	if previousApostrophe {
		return fmt.Errorf("invalid trailing Pinyin apostrophe")
	}
	return nil
}

func (decoder *Decoder) tokenize(prompt string) []spellingToken {
	if strings.Contains(prompt, "'") {
		parts := strings.Split(prompt, "'")
		result := make([]spellingToken, 0, len(parts))
		for index, part := range parts {
			token, ok := decoder.explicitToken(part, index == len(parts)-1)
			if !ok {
				return nil
			}
			result = append(result, token)
		}
		return result
	}
	type tokenization struct {
		tokens []spellingToken
		score  int
		ok     bool
	}
	memo := make(map[int]tokenization)
	var visit func(int) tokenization
	visit = func(position int) tokenization {
		if position == len(prompt) {
			return tokenization{ok: true}
		}
		if cached, exists := memo[position]; exists {
			return cached
		}
		best := tokenization{}
		consider := func(end int, token spellingToken, weight int) {
			tail := visit(end)
			if !tail.ok {
				return
			}
			next := tokenization{tokens: append([]spellingToken{token}, tail.tokens...), score: weight + tail.score, ok: true}
			if !best.ok || len(next.tokens) < len(best.tokens) || len(next.tokens) == len(best.tokens) && next.score > best.score {
				best = next
			}
		}
		for end := len(prompt); end > position; end-- {
			part := prompt[position:end]
			if len(part) == 1 && isInitial(part[0]) {
				consider(end, spellingToken{part, tokenInitial}, 1)
			}
			if _, exists := decoder.syllables[part]; exists && !(len(part) == 1 && isInitial(part[0])) {
				consider(end, spellingToken{part, tokenFull}, 4+len(part))
			}
			if end == len(prompt) {
				if _, exists := decoder.prefixes[part]; exists {
					consider(end, spellingToken{part, tokenPrefix}, 2+len(part))
				}
			}
		}
		memo[position] = best
		return best
	}
	return visit(0).tokens
}

func (decoder *Decoder) explicitToken(part string, final bool) (spellingToken, bool) {
	if len(part) == 1 && isInitial(part[0]) {
		return spellingToken{part, tokenInitial}, true
	}
	if _, exists := decoder.syllables[part]; exists {
		return spellingToken{part, tokenFull}, true
	}
	if final {
		if _, exists := decoder.prefixes[part]; exists {
			return spellingToken{part, tokenPrefix}, true
		}
	}
	return spellingToken{}, false
}

func isInitial(letter byte) bool {
	return strings.ContainsRune("bpmfdtnlgkhjqxzcsryw", rune(letter))
}

func matchSpellings(syllables []string, tokens []spellingToken) bool {
	if len(syllables) != len(tokens) {
		return false
	}
	for index, token := range tokens {
		syllable := syllables[index]
		switch token.kind {
		case tokenFull:
			if syllable != token.text {
				return false
			}
		case tokenPrefix:
			if !strings.HasPrefix(syllable, token.text) {
				return false
			}
		case tokenInitial:
			if syllable[0] != token.text[0] {
				return false
			}
		}
	}
	return true
}

func (decoder *Decoder) compose(tokens []spellingToken) string {
	type state struct {
		text  string
		score float64
		ok    bool
	}
	best := make([]state, len(tokens)+1)
	best[len(tokens)] = state{ok: true}
	for position := len(tokens) - 1; position >= 0; position-- {
		maximum := min(len(tokens)-position, len(decoder.entries)-1)
		for length := 1; length <= maximum; length++ {
			if !best[position+length].ok {
				continue
			}
			for _, candidate := range decoder.entries[length] {
				if !matchSpellings(candidate.syllables, tokens[position:position+length]) {
					continue
				}
				score := math.Log(candidate.frequency) + best[position+length].score
				text := candidate.text + best[position+length].text
				if !best[position].ok || score > best[position].score || score == best[position].score && text < best[position].text {
					best[position] = state{text, score, true}
				}
			}
		}
	}
	return best[0].text
}
