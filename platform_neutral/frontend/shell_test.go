package frontend

import (
	"path/filepath"
	"strings"
	"testing"

	"st-go/config"
	"st-go/platform_neutral/api"
)

func TestStChildEnvExportsFileBrowserIcons(t *testing.T) {
	t.Setenv("ST_GO_FILE_BROWSER_ICON_DIRECTORY", "inherited")
	t.Setenv("ST_GO_EXECUTABLE", "/stale/st")
	t.Setenv("ST_GO_BACKEND", "inherited-backend")
	t.Setenv("ST_GO_NATIVE_VIDEO", "forged")
	t.Setenv("ST_GO_NATIVE_VIDEO_TOKEN", "stale")
	t.Setenv("ST_GO_NATIVE_AUDIO", "forged")
	t.Setenv("ST_GO_NATIVE_AUDIO_TOKEN", "stale")
	t.Setenv("ST_GO_TABS", "1")
	t.Setenv("ST_GO_TAB_TOKEN", "stale")
	t.Setenv("ST_GO_CLIPBOARD_SYNC_TOKEN", "stale")
	t.Setenv("ST_FILE_BROWSER_ICON_DIRECTORY", "user override")
	cfg := config.Default()
	cfg.GeometryToken = "geometry-secret"
	cfg.NativeVideoToken = "fresh-native-secret"
	cfg.NativeAudioToken = "fresh-audio-secret"
	cfg.ClipboardSyncToken = "fresh-clipboard-secret"
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
	if _, ok := values["ST_GO_BACKEND"]; ok {
		t.Fatal("backend must be supplied by the actual host, not inherited")
	}
	if _, ok := values["ST_GO_NATIVE_VIDEO"]; ok {
		t.Fatal("inherited native-video support was not removed")
	}
	if values["ST_GO_NATIVE_VIDEO_TOKEN"] != "fresh-native-secret" {
		t.Fatal("native video capability was not replaced")
	}
	if values["ST_GO_NATIVE_AUDIO_TOKEN"] != "fresh-audio-secret" || values["ST_GO_NATIVE_AUDIO"] != "1" {
		t.Fatal("native audio capability was not replaced")
	}
	if values["ST_GO_CLIPBOARD_SYNC_TOKEN"] != "fresh-clipboard-secret" {
		t.Fatal("clipboard synchronization capability was not replaced")
	}
	if values["ST_GO_TABS"] != "" || values["ST_GO_TAB_TOKEN"] != "" {
		t.Fatal("disabled tabs inherited a stale capability")
	}
	cfg.TabSpawnToken = "fresh-tab-secret"
	joined := strings.Join(stChildEnv(cfg), "\n")
	if !strings.Contains(joined, "ST_GO_TABS=1\n") || !strings.Contains(joined, "ST_GO_TAB_TOKEN=fresh-tab-secret") {
		t.Fatal("enabled tab capability missing")
	}
}

type namedBackendHost struct {
	api.Host
	name string
}

func (h namedBackendHost) BackendName() string { return h.name }

func TestBackendChildEnvironmentReplacesInheritedIdentity(t *testing.T) {
	for _, name := range []string{"x11", "fb", "web", "unrecognized"} {
		t.Run(name, func(t *testing.T) {
			env := backendChildEnv([]string{"ST_GO_BACKEND=x11", "KEEP=yes", "ST_GO_BACKEND=web"}, namedBackendHost{name: name})
			want := name
			if name == "unrecognized" {
				want = "unknown"
			}
			count := 0
			for _, entry := range env {
				if strings.HasPrefix(entry, "ST_GO_BACKEND=") {
					count++
					if entry != "ST_GO_BACKEND="+want {
						t.Fatal("wrong backend identity", entry)
					}
				}
			}
			if count != 1 || env[0] != "KEEP=yes" {
				t.Fatal("backend environment was duplicated/lost unrelated values", env)
			}
		})
	}
	if env := backendChildEnv([]string{"ST_GO_BACKEND=x11"}, nil); len(env) != 1 || env[0] != "ST_GO_BACKEND=unknown" {
		t.Fatal("unidentified host inherited desktop privileges", env)
	}
}
