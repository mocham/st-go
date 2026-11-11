package sharedclipboard

import (
	"net/url"

	_ "modernc.org/sqlite"
)

// All backends share the same SQLite driver and on-disk clipboard format.
const sqliteDriver = "sqlite"

func driverParams() url.Values {
	return url.Values{"_pragma": {"busy_timeout(250)", "journal_mode(WAL)", "secure_delete(ON)", "synchronous(NORMAL)"}}
}
