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

type NativeAudioHooks interface {
	NativeAudio(id string, request api.NativeAudioRequest)
}

func (t *Term) dslNativeAudio(args []string) {
	if len(args) != 4 || args[0] != "auth" || t.cfg.NativeAudioToken == "" ||
		subtle.ConstantTimeCompare([]byte(args[1]), []byte(t.cfg.NativeAudioToken)) != 1 ||
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
	var request api.NativeAudioRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Validate() != nil {
		return
	}
	if hooks, ok := t.hooks.(NativeAudioHooks); ok {
		hooks.NativeAudio(args[2], request)
	}
}
