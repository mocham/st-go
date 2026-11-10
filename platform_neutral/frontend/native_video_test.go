package frontend

import (
	"strings"
	"testing"

	"st-go/platform_neutral/api"
)

type nativeVideoTestHost struct {
	api.Host
	request api.NativeVideoRequest
	done    func(error)
}

func (h *nativeVideoTestHost) Close() {}

func (h *nativeVideoTestHost) StartNativeVideo(r api.NativeVideoRequest, done func(error)) error {
	h.request, h.done = r, done
	return nil
}

func TestNativeVideoDispatchAndSerializedReplies(t *testing.T) {
	trm := regionTestTerminal(t)
	host := &nativeVideoTestHost{}
	trm.host = host
	var output strings.Builder
	trm.termCore.SetWriter(func(data []byte) { output.Write(data) })
	r := api.NativeVideoRequest{File: "/safe/movie.mp4", ALSAConfig: "/safe/alsa.conf", AudioDevice: "alsa/plughw:CARD=1,DEV=0", Volume: 100}
	id := "00112233445566778899aabbccddeeff"
	trm.mu.Lock()
	trm.NativeVideo(id, r)
	trm.mu.Unlock()
	if host.request != r || host.done == nil || !strings.Contains(output.String(), id+" accepted") {
		t.Fatal("native request was not queued/acknowledged")
	}
	host.done(nil)
	if !strings.Contains(output.String(), id+" done") {
		t.Fatal("native completion was not returned to the waiting terminal client")
	}
}
