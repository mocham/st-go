//go:build cgo && !backend_web

package sharedclipboard

import (
	_ "github.com/mattn/go-sqlite3"
	"net/url"
)

const sqliteDriver = "sqlite3"

func driverParams() url.Values {
	return url.Values{"_busy_timeout": {"250"}, "_journal_mode": {"WAL"}, "_secure_delete": {"on"}, "_synchronous": {"NORMAL"}}
}
