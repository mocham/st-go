// Package textsearch expands literal Unicode search shortcuts shared by TUIs.
package textsearch

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var greekNames = map[string]string{
	"alpha": "α", "beta": "β", "gamma": "γ", "delta": "δ", "epsilon": "ε", "zeta": "ζ", "eta": "η", "theta": "θ",
	"iota": "ι", "kappa": "κ", "lambda": "λ", "mu": "μ", "nu": "ν", "xi": "ξ", "omicron": "ο", "pi": "π",
	"rho": "ρ", "sigma": "σ", "tau": "τ", "upsilon": "υ", "phi": "φ", "chi": "χ", "psi": "ψ", "omega": "ω",
	"varepsilon": "ϵ", "vartheta": "ϑ", "varpi": "ϖ", "varrho": "ϱ", "varsigma": "ς", "varphi": "ϕ",
}

var accents = map[byte]rune{
	'\'': '\u0301', '`': '\u0300', '^': '\u0302', '"': '\u0308', '~': '\u0303', '=': '\u0304', '.': '\u0307',
	'c': '\u0327', 'v': '\u030c', 'u': '\u0306', 'H': '\u030b', 'r': '\u030a', 'k': '\u0328',
}

// Expand recognizes {alpha} and LaTeX-style accents such as \'e. Unknown
// names/commands stay literal; escaped braces and doubled backslashes are literal.
// The result is NFC-normalized, and no shell or regular-expression syntax runs.
func Expand(source string) string {
	var out strings.Builder
	for i := 0; i < len(source); {
		if source[i] == '{' {
			if end := strings.IndexByte(source[i:], '}'); end >= 0 && end <= 16 {
				name := source[i+1 : i+end]
				if letter, ok := greekNames[strings.ToLower(name)]; ok {
					if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
						letter = strings.ToUpper(letter)
					}
					out.WriteString(letter)
					i += end + 1
					continue
				}
			}
		}
		if source[i] == '\\' && i+1 < len(source) {
			op := source[i+1]
			if op == '\\' || op == '{' || op == '}' {
				out.WriteByte(op)
				i += 2
				continue
			}
			if accent := accents[op]; accent != 0 && i+2 < len(source) {
				start := i + 2
				braced := source[start] == '{'
				if braced {
					start++
				}
				letter, size := utf8.DecodeRuneInString(source[start:])
				end := start + size
				if unicode.IsLetter(letter) && (!braced || (end < len(source) && source[end] == '}')) && (braced || !unicode.IsLetter(rune(op))) {
					out.WriteRune(letter)
					out.WriteRune(accent)
					if braced {
						end++
					}
					i = end
					continue
				}
			}
		}
		r, size := utf8.DecodeRuneInString(source[i:])
		out.WriteRune(r)
		i += size
	}
	return norm.NFC.String(out.String())
}
