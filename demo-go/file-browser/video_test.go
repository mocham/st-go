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

func TestLegacyNativeVideoHelperPTY(t *testing.T) {
	// The explicit mpv helper remains available, independently of the default.
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "done", true: "failed"}[failed], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "movie ';--.MKV")
			writeFiles(t, dir, filepath.Base(path))
			config := filepath.Join(dir, "alsa.conf")
			if err := os.WriteFile(config, alsaConfData, 0600); err != nil {
				t.Fatal(err)
			}
			token := "00112233445566778899aabbccddeeff"
			env := testEnv("ST_GO_BACKEND=fb", "ST_GO_NATIVE_VIDEO=1", "ST_GO_NATIVE_VIDEO_TOKEN="+token, "ST_FILE_BROWSER_VOLUME=33", "ALSA_CONFIG_PATH="+config)
			for _, key := range []string{"ST_FILE_BROWSER_ALSA_DEVICE", "ALSA_CARD", "ALSA_PCM_CARD", "ALSA_PCM_DEVICE"} {
				env = envWithout(env, key)
			}
			f := startBrowserCommand(t, env, browserBin, "--native-video", path)
			var id string
			var request api.NativeVideoRequest
			waitFor(t, f, func() bool {
				out := f.output()
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
					t.Fatal("invalid handoff", fields)
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
			}, "authenticated legacy handoff")
			if request.File != path || request.AudioDevice != "alsa/plughw:1,0" || request.Volume != 33 || request.ALSAConfig != config {
				t.Fatal(request)
			}
			data, err := os.ReadFile(request.ALSAConfig)
			if err != nil || !bytes.Equal(data, alsaConfData) {
				t.Fatal("lost supplied ALSA config", err)
			}
			f.send("\x1bPnative-video " + id + " accepted\x1b\\")
			if failed {
				f.send("\x1bPnative-video " + id + " error " + base64.RawStdEncoding.EncodeToString([]byte("synthetic player failure")) + "\x1b\\")
				waitContains(t, f, "synthetic player failure", "helper reports failure")
			} else {
				f.send("\x1bPnative-video " + id + " done\x1b\\")
			}
		})
	}
}

func TestGov2VideoOpenerPTY(t *testing.T) {
	for implementation, program := range documentBrowserCommands(t) {
		for _, backend := range []string{"fb", "x11", "web"} {
			for _, mode := range []string{"enter", "open", "double", "missing", "custom", "failed"} {
				if backend != "fb" && (mode == "open" || mode == "double" || mode == "failed") {
					continue
				}
				t.Run(implementation+"/"+backend+"/"+mode, func(t *testing.T) {
					dir, tools := t.TempDir(), documentToolTestPath(t)
					name := "movie ';--.MP4"
					writeFiles(t, dir, name)
					marker := filepath.Join(tools, "args")
					if mode != "missing" {
						writeDocumentTool(t, tools, "gov2", `
[[ -t 0 && -t 1 && -t 2 ]] || exit 81
printf '%s|%s|%s|%s|%s' "$#" "$1" "$2" "$3" "$PWD" > "$MARKER"
stty -echo -icanon min 1 time 0
printf 'GOV2_READY'
IFS= read -rsn1 key
[[ $key == q ]] || exit 82
exit "${TOOL_EXIT:-0}"
`)
					}
					for _, other := range []string{"gov", "mpv", "bar", "xdg-open", "st"} {
						writeDocumentTool(t, tools, other, `printf 'WRONG_PLAYER' > "$MARKER"`)
					}
					env := envWithout(testEnv("PATH="+tools, "MARKER="+marker, "ST_GO_BACKEND="+backend, "ST_GO_NATIVE_VIDEO_TOKEN=secret", "ST_FILE_BROWSER_VOLUME=75"), "ST_FILE_BROWSER_OPEN")
					args := append([]string{}, program...)
					if mode == "custom" {
						args = append(args, "--open-for", backend, `printf 'custom' > "$MARKER"`)
					}
					if mode == "failed" {
						env = append(env, "TOOL_EXIT=7")
					}
					f := startBrowserCommand(t, env, append(args, dir)...)
					if mode == "double" {
						f.send("\x1b[<0;60;9M\x1b[<0;60;9M")
					} else if mode == "open" {
						f.send("\x1b[Bo")
					} else {
						f.send("\x1b[B\r")
					}
					if mode == "custom" {
						waitFileContent(t, marker, "custom")
						return
					}
					if backend != "fb" || mode == "missing" {
						message := "No video"
						if backend == "fb" {
							message = "gov2 not found"
						}
						waitContains(t, f, message, "video opener availability")
						if _, err := os.Stat(marker); !os.IsNotExist(err) {
							t.Fatal("unexpected fallback player")
						}
						return
					}
					waitFileContent(t, marker, "3|-volume|75|"+filepath.Join(dir, name)+"|"+dir)
					waitContains(t, f, "GOV2_READY", "gov2 client owns the PTY")
					f.send("q")
					result := "gov2 finished"
					if mode == "failed" {
						result = "gov2: exit" // The status line quotes/truncates the error.
						if implementation == "bash" {
							result = "gov2 exited"
						}
					}
					waitContains(t, f, result, "browser restored after gov2")
					f.send("r")
					waitContains(t, f, "Refreshed", "browser input restored")
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
