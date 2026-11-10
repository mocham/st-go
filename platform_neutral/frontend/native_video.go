package frontend

import (
	"encoding/base64"
	"fmt"

	"st-go/platform_neutral/api"
)

// Called by the parser with t.mu held. No platform-specific handles or console
// operations cross this boundary; only the framebuffer host implements it.
func (t *Terminal) NativeVideo(id string, request api.NativeVideoRequest) {
	reply := func(status string, err error) {
		message := ""
		if err != nil {
			message = base64.RawStdEncoding.EncodeToString([]byte(err.Error()[:min(len(err.Error()), 2048)]))
		}
		t.termCore.WriteToTTY([]byte(fmt.Sprintf("\x1bPnative-video %s %s %s\x1b\\", id, status, message)), false)
	}
	host, ok := t.host.(api.NativeVideoHost)
	if !ok {
		reply("error", fmt.Errorf("this backend does not support native video"))
		return
	}
	err := host.StartNativeVideo(request, func(err error) {
		t.mu.Lock()
		defer t.mu.Unlock()
		if !t.closed() {
			if err != nil {
				reply("error", err)
			} else {
				reply("done", nil)
			}
		}
	})
	if err != nil {
		reply("error", err)
	} else {
		reply("accepted", nil)
	}
}
