package main

import "testing"

func TestSharedReadlineSearchCursor(t *testing.T) {
	b, _ := searchTestBrowser(t)
	b.startDocumentSearchPrompt('/')
	for _, character := range []byte("wrld\x01\x06o") {
		b.handleSearchPromptKey(character)
	}
	if string(b.searchPromptBuffer) != "/world" {
		t.Fatalf("search cursor editing=%q", b.searchPromptBuffer)
	}
	b.handleSearchPromptKey('\r')
	if b.documentSearch.query != "world" {
		t.Fatal("edited search was not submitted")
	}
	b.startDocumentSearchPrompt(':')
	for _, character := range []byte("nh\x01\x06o") {
		b.handleSearchPromptKey(character)
	}
	if string(b.searchPromptBuffer) != ":noh" {
		t.Fatalf("command cursor editing=%q", b.searchPromptBuffer)
	}
}
