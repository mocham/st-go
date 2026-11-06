package latexpdf

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

func TestAppleMacInputEncodingBecomesUTF8(t *testing.T) {
	utf8Source := `\documentclass{article}
\usepackage[applemac]{inputenc}
\title{Étude des représentations}
\begin{document}
On étudie la catégorie où les représentations sont semi-linéaires.
\end{document}`
	encoded, err := charmap.Macintosh.NewEncoder().String(utf8Source)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, "étudie") {
		t.Fatal("test fixture unexpectedly remained UTF-8")
	}
	result := newLatexRenderer().render(encoded, "applemac-french-source")
	body := string(result.Body)
	for _, wanted := range []string{"étudie", "catégorie", "où", "représentations", "semi-linéaires"} {
		if !strings.Contains(body, wanted) {
			t.Errorf("decoded source is missing %q: %s", wanted, body)
		}
	}
	if strings.ContainsRune(body, '�') {
		t.Fatalf("decoded source contains replacement characters: %s", body)
	}
	title, ok := FindCommandArgument(encoded, "title")
	if !ok || title != "Étude des représentations" {
		t.Fatalf("decoded title = %q, %t", title, ok)
	}
}

func TestFrenchTextAccentCommands(t *testing.T) {
	source := "\\begin{document}\n" +
		"L\\'etude d'un gar\\c{c}on, le r\\^ole na\\\"if, No\\\"el, d\\'ej\\`a, une \\oe{}uvre, \\og texte \\fg, \\~{}.\n" +
		"\\end{document}"
	result := newLatexRenderer().render(source, "french-text-accents")
	body := string(result.Body)
	for _, wanted := range []string{"Létude", "garçon", "rôle", "naïf", "Noël", "déjà", "œuvre", "«", "»", "~"} {
		if !strings.Contains(body, wanted) {
			t.Errorf("accented output is missing %q: %s", wanted, body)
		}
	}
	plain := PlainText("gar\\c{c}on, d\\'ej\\`a, \\oe{}uvre")
	if plain != "garçon, déjà, œuvre" {
		t.Fatalf("accented plain text = %q", plain)
	}
}
