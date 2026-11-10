package tabs

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"st-go/platform_neutral/api"
)

func TestRequestRoundTripsLiteralArguments(t *testing.T) {
	token, id := strings.Repeat("ab", 16), strings.Repeat("cd", 16)
	index := 1
	r := api.TabSpawnRequest{Command: []string{"/bin/tool", "/tmp/a ' ;\n世界"}, Directory: "/tmp", Detached: true, CopyFile: &index}
	frame, err := RequestFrame(token, id, r)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(frame, "\x1bP"), "\x1b\\"))
	if len(fields) != 5 || fields[0] != "tab-spawn" || fields[2] != token || fields[3] != id {
		t.Fatal("incorrect protocol frame", frame)
	}
	data, err := base64.RawStdEncoding.DecodeString(fields[4])
	if err != nil {
		t.Fatal(err)
	}
	var got api.TabSpawnRequest
	if json.Unmarshal(data, &got) != nil || !reflect.DeepEqual(r, got) {
		t.Fatal("literal argv did not round-trip", got)
	}
	r.Command[0] = "bad\x00program"
	if _, err := RequestFrame(token, id, r); err == nil {
		t.Fatal("accepted NUL in argv")
	}
}

func TestReplyValidation(t *testing.T) {
	id := strings.Repeat("ab", 16)
	for _, tc := range []struct {
		control string
		valid   bool
	}{
		{"tab-spawn " + id + " accepted 3", true},
		{"tab-spawn " + id + " supported 1", true},
		{"tab-spawn " + id + " supported 2", false},
		{"tab-spawn " + id + " accepted 0", false},
		{"tab-spawn " + id + " error " + base64.RawStdEncoding.EncodeToString([]byte("disabled")), true},
		{"tab-spawn invalid accepted 3", false},
		{"tab-spawn " + id + " accepted 3 extra", false},
	} {
		if _, ok := ParseReply(tc.control); ok != tc.valid {
			t.Fatal("incorrect reply validation", tc.control)
		}
	}
}
