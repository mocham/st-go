package stterminal

import "crypto/subtle"

type AppBusHooks interface{ AppBus([]string) }

func (t *Term) dslAppBus(args []string) {
	if len(args) < 4 || args[0] != "auth" || t.cfg.AppBusToken == "" || subtle.ConstantTimeCompare([]byte(args[1]), []byte(t.cfg.AppBusToken)) != 1 {
		return
	}
	if hooks, ok := t.hooks.(AppBusHooks); ok {
		hooks.AppBus(args[2:])
	}
}
