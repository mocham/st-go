package stterminal

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"

	"st-go/platform_neutral/api"
)

type VideoHooks interface {
	Video(id string, request api.VideoRequest)
}

// Reuse the seat-local video capability already supplied to framebuffer PTYs.
func (t *Term) dslVideo(args []string) {
	if len(args) != 4 || args[0] != "auth" || t.cfg.NativeVideoToken == "" ||
		subtle.ConstantTimeCompare([]byte(args[1]), []byte(t.cfg.NativeVideoToken)) != 1 ||
		len(args[2]) != 32 || len(args[3]) > 16384 {
		return
	}
	if _, err := hex.DecodeString(args[2]); err != nil {
		return
	}
	data, err := base64.RawStdEncoding.DecodeString(args[3])
	if err != nil {
		return
	}
	var request api.VideoRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Validate() != nil {
		return
	}
	if hooks, ok := t.hooks.(VideoHooks); ok {
		hooks.Video(args[2], request)
	}
}

// AlternateScreenHooks lets transient native media follow application lifetime.
type AlternateScreenHooks interface{ AlternateScreen(active bool) }
