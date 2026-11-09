package textsearch

import "testing"

func TestExpand(t *testing.T) {
	for _, test := range [][2]string{
		{"{alpha} {Gamma} {varsigma}", "α Γ ς"},
		{`caf\'e \'{E} \"u \v{c} \c{c}`, "café É ü č ç"},
		{"cafe\u0301", "café"},
		{`\{alpha\} \\'e {unknown} \unknown`, `{alpha} \'e {unknown} \unknown`},
		{`unfinished {alpha \'{`, `unfinished {alpha \'{`},
	} {
		if got := Expand(test[0]); got != test[1] {
			t.Errorf("Expand(%q)=%q,want %q", test[0], got, test[1])
		}
	}
}
