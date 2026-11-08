package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
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

func TestInvalidConfigFallsBack(t *testing.T) {
	palette16, _ := json.Marshal(map[string]any{"colorname": Default().Colorname[:16]})
	for name, contents := range map[string]string{
		"syntax":       `{"cols":12,`,
		"type":         `{"cols":12,"rows":"bad"}`,
		"short colors": string(palette16),
		"null colors":  `{"colorname":null}`,
		"zero cells":   `{"cols":0}`,
		"zero glyph":   `{"glyphwidth":0}`,
		"overflow":     `{"cols":18446744073709551615}`,
		"window limit": `{"cols":65535,"rows":65535}`,
		"border":       `{"borderpx":-1}`,
		"baseline":     `{"glyphbaseline":1000}`,
		"tabs":         `{"tabspaces":0}`,
		"latency":      `{"minlatency":86400000,"maxlatency":86400000}`,
		"color index":  `{"defaultfg":4294967295}`,
		"invisible":    `{"defaultfg":259}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := Load(path)
			if err == nil {
				t.Fatal("unusable config accepted")
			}
			if !reflect.DeepEqual(got, Default()) {
				t.Fatal("fallback retained invalid/partial settings")
			}
		})
	}
}

func TestConfigFileFailuresFallBack(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "missing"), dir, fifo} {
		got, err := Load(path)
		if err == nil || !reflect.DeepEqual(got, Default()) {
			t.Fatalf("%s: config=%+v err=%v", path, got, err)
		}
	}
}

func TestInstalledConfigResolution(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	check := func(explicit string, cols uint, wantError bool) {
		t.Helper()
		got, err := loadResolved(explicit, dir)
		if got.Cols != cols || (err != nil) != wantError {
			t.Fatalf("cols=%d, err=%v", got.Cols, err)
		}
	}
	check("", 80, false)
	write("config.json", `{"cols":37}`)
	check("", 37, false)
	write("st.json", `{"cols":42}`)
	check("", 42, false)
	explicit := write("explicit.json", `{"cols":53}`)
	check(explicit, 53, false)
	write("st.json", `{"cols":0}`)
	check("", 80, true) // broken preferred config falls back to embedded defaults
	check(filepath.Join(dir, "missing"), 80, true)
}

func TestConfigRelativeFont(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "st.json")
	if err := os.WriteFile(path, []byte(`{"font":"fonts/custom.ttf"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Font != filepath.Join(dir, "fonts", "custom.ttf") {
		t.Fatalf("font path = %q", cfg.Font)
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
