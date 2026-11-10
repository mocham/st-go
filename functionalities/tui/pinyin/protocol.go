// Package pinyin provides a bounded, asynchronous protocol for an isolated
// input-method process. It contains no native code or display dependencies.
package pinyin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"unicode"
	"unicode/utf8"
)

const MaxPrompt = 32 // below Google Pinyin's 40-step native workspace
const PageSize = 5
const MaxCandidates = 4096
const MaxPage = (MaxCandidates - 1) / PageSize
const MaxMessage = 4096

type Request struct {
	ID     uint64 `json:"id"`
	Prompt string `json:"prompt"`
	Page   int    `json:"page"`
}
type Response struct {
	ID         uint64   `json:"id,omitempty"`
	Prompt     string   `json:"prompt,omitempty"`
	Page       int      `json:"page,omitempty"`
	Total      int      `json:"total,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	Ready      bool     `json:"ready,omitempty"`
	Error      string   `json:"error,omitempty"`
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
	if r.Error != "" {
		return fmt.Errorf("pinyin helper: %s", r.Error)
	}
	if r.Ready || r.ID != request.ID || r.Prompt != request.Prompt || r.Total < 0 || r.Total > MaxCandidates || r.Page < 0 || r.Page > MaxPage || len(r.Candidates) > PageSize {
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
func Scanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 512), MaxMessage)
	return s
}
func Read(s *bufio.Scanner, v any) error {
	if !s.Scan() {
		if err := s.Err(); err != nil {
			return err
		}
		return io.EOF
	}
	return json.Unmarshal(s.Bytes(), v)
}
func Write(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b)+1 >= MaxMessage {
		return fmt.Errorf("pinyin message exceeds limit")
	}
	b = append(b, '\n')
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
