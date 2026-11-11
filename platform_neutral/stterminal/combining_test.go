package stterminal

import (
	"strings"
	"testing"
)

func TestCombiningSequencesRetainGridSelectionAndOverwrite(t *testing.T) {
	trm, _ := newTestTerm(t, 8, 2)
	trm.Twrite([]byte("e\u0301界\u0301X"), false)
	if trm.line[0][0].Text() != "e\u0301" || trm.line[0][1].Text() != "界\u0301" || trm.line[0][3].U != 'X' || trm.c.x != 4 {
		t.Fatal("combining marks changed grid advances", trm.line[0], trm.c)
	}
	trm.SelStart(0, 0, 0)
	trm.SelExtend(3, 0, SelRegular, 0)
	trm.SelExtend(3, 0, SelRegular, 1)
	if trm.GetSel() != "e\u0301界\u0301X" {
		t.Fatal("selection lost combining marks", trm.GetSel())
	}
	trm.Twrite([]byte("\rZ"+strings.Repeat("\u0301", 100)), false)
	if trm.line[0][0].U != 'Z' || len(trm.line[0][0].Suffix) > 64 {
		t.Fatal("unbounded combining sequence")
	}
	trm.Twrite([]byte("\rQ"), false)
	if trm.line[0][0].Suffix != "" {
		t.Fatal("overwrite retained old marks")
	}
}
