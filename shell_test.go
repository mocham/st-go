package main

import (
	"path/filepath"
	"strings"
	"testing"

	"st-go/config"
)

func TestStChildEnvExportsFileBrowserIcons(t *testing.T) {
	t.Setenv("ST_GO_FILE_BROWSER_ICON_DIRECTORY", "inherited")
	t.Setenv("ST_GO_EXECUTABLE", "/stale/st")
	t.Setenv("ST_FILE_BROWSER_ICON_DIRECTORY", "user override")
	cfg := config.Default()
	cfg.GeometryToken = "geometry-secret"
	cfg.FileBrowser.Icons.Directory = "configured"

	env := stChildEnv(cfg)
	values := make(map[string]string, len(env))
	generatedCount := 0
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		values[name] = value
		if name == "ST_GO_FILE_BROWSER_ICON_DIRECTORY" {
			generatedCount++
		}
	}

	if got := values["ST_GO_FILE_BROWSER_ICON_DIRECTORY"]; got != "configured" {
		t.Fatalf("generated directory icon = %q, want configured", got)
	}
	if generatedCount != 1 {
		t.Fatalf("generated directory icon occurs %d times, want once", generatedCount)
	}
	if got := values["ST_FILE_BROWSER_ICON_DIRECTORY"]; got != "user override" {
		t.Fatalf("user directory icon override = %q, want preserved", got)
	}
	if got := values["ST_GO_FILE_BROWSER_ICON_PARENT"]; got != cfg.FileBrowser.Icons.Parent {
		t.Fatalf("generated parent icon = %q, want %q", got, cfg.FileBrowser.Icons.Parent)
	}
	if executable := values["ST_GO_EXECUTABLE"]; !filepath.IsAbs(executable) || executable == "/stale/st" {
		t.Fatalf("ST_GO_EXECUTABLE = %q, want current absolute executable", executable)
	}
	if got := values["ST_GO_GEOMETRY_TOKEN"]; got != "geometry-secret" {
		t.Fatalf("ST_GO_GEOMETRY_TOKEN = %q", got)
	}
}
