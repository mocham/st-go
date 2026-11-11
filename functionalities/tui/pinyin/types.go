// Package pinyin provides bounded requests, paging, and asynchronous workers
// for the in-process pure-Go Pinyin decoder. It has no display dependencies.
package pinyin

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

const MaxPrompt = 32 // shared protocol and pure-Go decoder bound
const PageSize = 5
const MaxCandidates = 4096
const MaxPage = (MaxCandidates - 1) / PageSize

type Request struct {
	ID     uint64
	Prompt string
	Page   int
}
type Response struct {
	ID         uint64
	Prompt     string
	Page       int
	Total      int
	Candidates []string
}

func (r Request) Validate() error {
	if r.ID == 0 || len(r.Prompt) == 0 || len(r.Prompt) > MaxPrompt || r.Page < 0 || r.Page > MaxPage {
		return fmt.Errorf("invalid pinyin request (maximum prompt %d bytes)", MaxPrompt)
	}
	for _, c := range r.Prompt {
		if (c < 'a' || c > 'z') && c != '\'' {
			return fmt.Errorf("pinyin accepts lowercase letters and apostrophes only")
		}
	}
	return nil
}
func (r Response) Validate(request Request) error {
	if r.ID != request.ID || r.Prompt != request.Prompt || r.Total < 0 || r.Total > MaxCandidates || r.Page < 0 || r.Page > MaxPage || len(r.Candidates) > PageSize {
		return fmt.Errorf("invalid pinyin response")
	}
	page := 0
	if r.Total > 0 {
		page = min(request.Page, (r.Total-1)/PageSize)
	}
	want := min(PageSize, max(0, r.Total-page*PageSize))
	if r.Page != page || len(r.Candidates) != want {
		return fmt.Errorf("invalid pinyin candidate page")
	}
	for _, s := range r.Candidates {
		if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxPrompt {
			return fmt.Errorf("invalid pinyin candidate")
		}
		for _, c := range s {
			if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
				return fmt.Errorf("pinyin candidate contains control text")
			}
		}
	}
	return nil
}
