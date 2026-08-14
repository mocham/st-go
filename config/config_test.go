package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultFileBrowserIcons(t *testing.T) {
	want := FileBrowserIcons{
		Parent:     "↑",
		Directory:  "▸",
		Symlink:    "↗",
		Image:      "▣",
		PDF:        "▤",
		Text:       "≡",
		Archive:    "▦",
		Audio:      "♪",
		Video:      "▶",
		Code:       "λ",
		Config:     "⚙",
		Executable: "◆",
		Default:    "·",
	}
	if got := Default().FileBrowser.Icons; !reflect.DeepEqual(got, want) {
		t.Fatalf("default file browser icons = %#v, want %#v", got, want)
	}
}

func TestLoadPartialFileBrowserIcons(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"file_browser":{"icons":{"directory":"D","code":"C"}}}`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FileBrowser.Icons.Directory != "D" || cfg.FileBrowser.Icons.Code != "C" {
		t.Fatalf("partial icon overrides not loaded: %#v", cfg.FileBrowser.Icons)
	}
	if cfg.FileBrowser.Icons.Parent != Default().FileBrowser.Icons.Parent {
		t.Fatalf("partial load replaced parent default with %q", cfg.FileBrowser.Icons.Parent)
	}
}

func TestWebPCachePath(t *testing.T) {
	if got := Default().WebPCachePath; got != "/tmp/st-go-webp-cache-{uid}/cache.sqlite3" {
		t.Fatalf("default WebP cache path = %q", got)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"webp_cache_path":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebPCachePath != "" {
		t.Fatalf("WebP cache path override = %q, want disabled", cfg.WebPCachePath)
	}
}
