package audio

import (
	"fmt"
	"os"
)

// ALSA diagnostics are opt-in so they can never corrupt a terminal display.
func alsaEmitError(message string) {
	path := os.Getenv("ST_GO_ALSA_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(f, message)
	_ = f.Close()
}
