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

type NativeVideoHooks interface {
	NativeVideo(id string, request api.NativeVideoRequest)
}

// Base64 JSON avoids the display DSL's filename quoting/semicolon limitations.
// Authentication is independent of desktop window-geometry permissions.
func (t *Term) dslNativeVideo(args []string) {
	if len(args) != 4 || args[0] != "auth" || t.cfg.NativeVideoToken == "" ||
		subtle.ConstantTimeCompare([]byte(args[1]), []byte(t.cfg.NativeVideoToken)) != 1 {
		return
	}
	if len(args[2]) != 32 || len(args[3]) > 32768 {
		return
	}
	if _, err := hex.DecodeString(args[2]); err != nil {
		return
	}
	data, err := base64.RawStdEncoding.DecodeString(args[3])
	if err != nil {
		return
	}
	var request api.NativeVideoRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Validate() != nil {
		return
	}
	if hooks, ok := t.hooks.(NativeVideoHooks); ok {
		hooks.NativeVideo(args[2], request)
	}
}
