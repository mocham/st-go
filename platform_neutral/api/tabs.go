package api

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TabSpawnRequest launches argv directly on a new PTY, never through a shell.
// Detached appends a background tab without changing the active tab. CopyFile
// optionally names an argv index whose temporary file must outlive the client;
// the terminal copies it to terminal-owned storage before acknowledging spawn.
type TabSpawnRequest struct {
	Probe     bool     `json:"probe,omitempty"` // read-only capability check; never starts a child
	Command   []string `json:"command,omitempty"`
	Directory string   `json:"directory,omitempty"`
	Title     string   `json:"title,omitempty"`
	Detached  bool     `json:"detached"`
	CopyFile  *int     `json:"copy_file,omitempty"`
}

const TabRequestLimit = 32768

func (r TabSpawnRequest) Validate() error {
	if r.Probe {
		if len(r.Command) != 0 || r.Directory != "" || r.Title != "" || r.Detached || r.CopyFile != nil {
			return fmt.Errorf("invalid tab capability probe")
		}
		return nil
	}
	if len(r.Command) == 0 || len(r.Command) > 128 || r.Command[0] == "" ||
		!filepath.IsAbs(r.Directory) || !utf8.ValidString(r.Title) || len(r.Title) > 256 {
		return fmt.Errorf("invalid tab command, directory or title")
	}
	size := len(r.Title)
	for _, s := range append([]string{r.Directory}, r.Command...) {
		if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return fmt.Errorf("invalid tab command or directory")
		}
		size += len(s)
	}
	if size > 20000 {
		return fmt.Errorf("tab request exceeds protocol limit")
	}
	for _, ch := range r.Title {
		if unicode.IsControl(ch) {
			return fmt.Errorf("invalid tab title")
		}
	}
	if r.CopyFile != nil && (*r.CopyFile < 1 || *r.CopyFile >= len(r.Command) || !filepath.IsAbs(r.Command[*r.CopyFile])) {
		return fmt.Errorf("invalid temporary tab file argument")
	}
	return nil
}
