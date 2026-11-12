package stterminal

import (
	"strconv"
	"time"
)

// refresh INTERVAL_MS DURATION_MS requests a temporary presentation cadence.
// It does not cause periodic redraws. Renew explicitly; refresh 0 0 cancels.
func (t *Term) dslRefresh(args []string) {
	if len(args) != 2 {
		return
	}
	interval, e1 := strconv.Atoi(args[0])
	duration, e2 := strconv.Atoi(args[1])
	if e1 != nil || e2 != nil {
		return
	}
	if !(interval == 0 && duration == 0) && (interval < 16 || interval > 1000 || duration < 1 || duration > 120000) {
		return
	}
	if h, ok := t.hooks.(interface {
		RequestRefresh(time.Duration, time.Duration)
	}); ok {
		h.RequestRefresh(time.Duration(interval)*time.Millisecond, time.Duration(duration)*time.Millisecond)
	}
}
