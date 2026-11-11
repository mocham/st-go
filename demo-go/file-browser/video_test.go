package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"st-go/platform_neutral/api"
)

func TestVideoClassification(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		name string
		want bool
	}{{"movie.MP4", true}, {"movie.mkv", true}, {"movie.m4v", true}, {"movie.m2ts", true}, {"movie.rmvb", true}, {"movie.MXF", true}, {"movie.wmv", true}, {"source.ts", false}, {"notes.txt", false}, {"picture.png", false}} {
		path := filepath.Join(dir, test.name)
		if err := os.WriteFile(path, []byte("ordinary text\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if got := videoFile(path); got != test.want {
			t.Fatalf("videoFile(%q)=%t, want %t", test.name, got, test.want)
		}
	}
}

func TestFramebufferVideoPlayerPTY(t *testing.T) {
	for implementation, program := range documentBrowserCommands(t) {
		for _, backend := range []string{"fb", "x11", "web"} {
			for _, mode := range []string{"default", "missing", "custom", "failed"} {
				if mode == "failed" && backend != "fb" {
					continue
				}
				t.Run(implementation+"/"+backend+"/"+mode, func(t *testing.T) {
					dir, tools := t.TempDir(), documentToolTestPath(t)
					name := "movie ';--.MKV"
					writeFiles(t, dir, name)
					marker := filepath.Join(tools, "args")
					providedConfig := filepath.Join(tools, "provided-alsa.conf")
					configData := append(append([]byte(nil), alsaConfData...), []byte("\n# working inherited configuration\n")...)
					if err := os.WriteFile(providedConfig, configData, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(browserBin, filepath.Join(tools, "file-browser")); err != nil {
						t.Fatal(err)
					}
					if mode != "missing" {
						writeDocumentTool(t, tools, "mpv", `printf 'FORBIDDEN_PTY_MPV' > "$MARKER"`)
					}
					for _, desktop := range []string{"bar", "xdg-open", "st"} {
						writeDocumentTool(t, tools, desktop, `printf 'FORBIDDEN_DESKTOP_FALLBACK' > "$MARKER"`)
					}
					token := "00112233445566778899aabbccddeeff"
					env := envWithout(testEnv("PATH="+tools, "MARKER="+marker, "ST_GO_EXECUTABLE="+filepath.Join(tools, "st-fb"),
						"ST_GO_BACKEND="+backend, "ST_GO_NATIVE_VIDEO=1", "ST_GO_NATIVE_VIDEO_TOKEN="+token,
						"ST_FILE_BROWSER_VOLUME=33", "ALSA_CONFIG_PATH="+providedConfig), "ST_FILE_BROWSER_OPEN")
					for _, key := range []string{"ST_FILE_BROWSER_ALSA_DEVICE", "ALSA_CARD", "ALSA_PCM_CARD", "ALSA_PCM_DEVICE"} {
						env = envWithout(env, key)
					}
					args := append([]string{}, program...)
					if mode == "custom" {
						args = append(args, "--open-for", backend, `printf 'custom' > "$MARKER"`)
					}
					f := startBrowserCommand(t, env, append(args, dir)...)
					before := len(f.output())
					f.send("\x1b[<0;60;9M\x1b[<0;60;9M")
					if mode == "custom" {
						waitFileContent(t, marker, "custom")
						if strings.Contains(f.output(), "native-video auth") {
							t.Fatal("mpv overrode explicit platform registration")
						}
						return
					}
					if backend != "fb" || mode == "missing" {
						message := "No video"
						if backend == "fb" {
							message = "mpv not found"
						}
						waitContains(t, f, message, "no default video player/fallback")
						if _, err := os.Stat(marker); !os.IsNotExist(err) {
							t.Fatal("non-framebuffer/missing video player launched an opener")
						}
						return
					}
					var id string
					var request api.NativeVideoRequest
					waitFor(t, f, func() bool {
						out := f.output()[before:]
						start := strings.Index(out, "\x1bPnative-video auth ")
						if start < 0 {
							return false
						}
						end := strings.Index(out[start:], "\x1b\\")
						if end < 0 {
							return false
						}
						fields := strings.Fields(out[start+2 : start+end])
						if len(fields) != 5 || fields[2] != token {
							t.Fatal("invalid native handoff frame", fields)
						}
						id = fields[3]
						data, err := base64.RawStdEncoding.DecodeString(fields[4])
						if err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(data, &request); err != nil {
							t.Fatal(err)
						}
						return true
					}, "authenticated native VT handoff")
					if request.File != filepath.Join(dir, name) || request.AudioDevice != "alsa/plughw:1,0" || request.Volume != 33 || request.ALSAConfig != providedConfig {
						t.Fatal("incorrect media/audio selection", request)
					}
					data, err := os.ReadFile(request.ALSAConfig)
					if err != nil || !bytes.Equal(data, configData) {
						t.Fatal("mpv did not receive the supplied ALSA config", err)
					}
					if strings.Contains(f.output()[before:], "place bottom-left") {
						t.Fatal("foreground DRM video collapsed the terminal")
					}
					if _, err := os.Stat(marker); !os.IsNotExist(err) {
						t.Fatal("client launched mpv on a PTY")
					}
					f.send("\x1bPnative-video " + id + " accepted\x1b\\")
					if strings.Contains(f.output()[before:], "region reset") {
						t.Fatal("browser resumed before native player finished")
					}
					if mode == "failed" {
						f.send("\x1bPnative-video " + id + " error " + base64.RawStdEncoding.EncodeToString([]byte("synthetic player failure")) + "\x1b\\")
					} else {
						f.send("\x1bPnative-video " + id + " done\x1b\\")
					}
					waitContains(t, f, "region reset", "browser restoration after mpv exit/failure")
					f.send("r")
					waitContains(t, f, "Refreshed", "browser input restored after mpv")
				})
			}
		}
	}
}

func TestVideoAudioSelectionDefaultsToCardOne(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{}, "alsa/plughw:1,0"},
		{map[string]string{"ALSA_CARD": "Generic_1"}, "alsa/plughw:CARD=Generic_1,DEV=0"},
		{map[string]string{"ALSA_CARD": "Generic", "ALSA_PCM_CARD": "Generic_1", "ALSA_PCM_DEVICE": "7"}, "alsa/plughw:CARD=Generic_1,DEV=7"},
		{map[string]string{"ALSA_PCM_DEVICE": "3"}, "alsa/plughw:CARD=1,DEV=3"},
		{map[string]string{"ST_FILE_BROWSER_ALSA_DEVICE": "plughw:1,3"}, "alsa/plughw:1,3"},
		{map[string]string{"ST_FILE_BROWSER_ALSA_DEVICE": "alsa/plughw:0,0", "ALSA_PCM_CARD": "1"}, "alsa/plughw:0,0"},
	} {
		got, err := videoAudioDevice(func(key string) string { return tc.env[key] })
		if err != nil || got != tc.want {
			t.Fatal("wrong PCM selection", got, err)
		}
	}
	for _, env := range []map[string]string{
		{"ALSA_CARD": "bad/card"},
		{"ALSA_PCM_DEVICE": "-1"},
		{"ALSA_PCM_DEVICE": "invalid"},
	} {
		if _, err := videoAudioDevice(func(key string) string { return env[key] }); err == nil {
			t.Fatal("accepted invalid PCM selection", env)
		}
	}
}

func TestVideoPlaybackVolume(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
		valid bool
	}{{"", 33, true}, {"0", 0, true}, {"75", 75, true}, {"100", 100, true}, {"101", 0, false}, {"-1", 0, false}, {"invalid", 0, false}} {
		got, err := playbackVolume(tc.value)
		if (err == nil) != tc.valid || (tc.valid && got != tc.want) {
			t.Fatal("incorrect playback volume", got, err)
		}
	}
}
