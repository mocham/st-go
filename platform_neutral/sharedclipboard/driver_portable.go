//go:build !cgo || backend_web

package sharedclipboard

import (
	_ "modernc.org/sqlite"
	"net/url"
)

// Clipboard synchronization remains compatible with native terminals' cache.
// This driver is unrelated to the native-only decoded-WebP pixel cache.
const sqliteDriver = "sqlite"

func driverParams() url.Values {
	return url.Values{"_pragma": {"busy_timeout(250)", "journal_mode(WAL)", "secure_delete(ON)", "synchronous(NORMAL)"}}
}
