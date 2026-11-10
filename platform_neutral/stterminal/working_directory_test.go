package stterminal

import (
	"path/filepath"
	"testing"

	"st-go/config"
)

func TestInitialWorkingDirectoryForRelativeDisplayResources(t *testing.T) {
	core := NewTerm(config.Default(), &mockHooks{})
	dir := t.TempDir()
	core.SetWorkingDirectory(dir)
	if got := core.resolveImagePath("preview.png"); got != filepath.Join(dir, "preview.png") {
		t.Fatalf("resolved path=%q", got)
	}
}
