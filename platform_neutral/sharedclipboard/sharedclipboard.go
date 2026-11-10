// Package sharedclipboard synchronizes opt-in clipboard state through the
// existing per-user SQLite cache. It has no listener, server, or process-wide
// coordinator: every terminal opens the database independently when enabled.
package sharedclipboard

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/sys/unix"
	"st-go/limits"
)

type Source uint8

const (
	SourceLocal Source = iota
	SourceXExternal
)

type Entry struct {
	Selection byte
	Text      string
	Revision  int64
	Writer    [16]byte
	Source    Source
	Display   string
}

type State struct {
	Enabled   bool
	Ready     bool
	Available bool
}

type Hooks struct {
	Changed func(Entry)
	Status  func(State)
}

var currentBootID = cacheBootID()

type commandKind uint8

const (
	commandEnable commandKind = iota
	commandDisable
	commandPublish
	commandQuery
	commandWatch
)

type command struct {
	kind      commandKind
	selection byte
	text      string
	source    Source
	display   string
	seed      map[byte]string
	reply     func(string, bool)
	watch     bool
}

// Sync owns one lazy database connection and a serialized operation queue.
// Disabled instances do not open SQLite and do not run a polling timer.
type Sync struct {
	path   string
	hooks  Hooks
	writer [16]byte

	requested atomic.Bool
	ready     atomic.Bool
	available atomic.Bool

	queueMu    sync.Mutex
	queue      []command
	queueBytes int
	wake       chan struct{}
	done       chan struct{}
	closed     bool
	once       sync.Once
}

func New(path string, hooks Hooks) *Sync {
	s := &Sync{path: path, hooks: hooks, wake: make(chan struct{}, 1), done: make(chan struct{})}
	s.available.Store(path != "")
	if _, err := rand.Read(s.writer[:]); err != nil {
		s.available.Store(false)
	}
	go s.run()
	return s
}

func (s *Sync) State() State {
	return State{Enabled: s.requested.Load(), Ready: s.ready.Load(), Available: s.available.Load()}
}

func validSelection(selection byte) bool { return selection == 'c' || selection == 'p' }

func (s *Sync) SetEnabled(enabled bool, seed map[byte]string) bool {
	if enabled && !s.available.Load() {
		return false
	}
	if s.requested.Swap(enabled) == enabled {
		return true
	}
	copySeed := make(map[byte]string, len(seed))
	if enabled {
		for selection, text := range seed {
			if validSelection(selection) && len(text) <= limits.ClipboardBytes && utf8.ValidString(text) {
				copySeed[selection] = text
			}
		}
	}
	kind := commandDisable
	if enabled {
		kind = commandEnable
	}
	if !s.enqueue(command{kind: kind, seed: copySeed}) {
		s.requested.Store(!enabled)
		return false
	}
	return true
}

func (s *Sync) Publish(selection byte, text string, source Source, display string) {
	if !s.requested.Load() || !validSelection(selection) || source > SourceXExternal || len(text) > limits.ClipboardBytes || !utf8.ValidString(text) {
		return
	}
	if len(display) > 512 || !utf8.ValidString(display) {
		display = ""
	}
	s.enqueue(command{kind: commandPublish, selection: selection, text: text, source: source, display: display})
}

func (s *Sync) Query(selection byte, reply func(string, bool)) {
	if reply == nil {
		return
	}
	if !s.requested.Load() || !validSelection(selection) || !s.enqueue(command{kind: commandQuery, selection: selection, reply: reply}) {
		go reply("", false)
	}
}

// SetWatching enables the small revision poll used only by adapters that must
// proactively project another process's clipboard (X11 and an attached web UI).
func (s *Sync) SetWatching(watch bool) { s.enqueue(command{kind: commandWatch, watch: watch}) }

func (s *Sync) Close() {
	s.once.Do(func() {
		s.requested.Store(false)
		s.ready.Store(false)
		s.queueMu.Lock()
		s.closed = true
		close(s.done)
		s.queueMu.Unlock()
	})
}

func (s *Sync) enqueue(c command) bool {
	s.queueMu.Lock()
	if s.closed {
		s.queueMu.Unlock()
		return false
	}
	// A clipboard source can generate repeated selections faster than SQLite
	// can acquire a busy cache. Coalesce writes that have no intervening read or
	// policy transition, and bound aggregate queued clipboard bytes.
	if c.kind == commandPublish {
		for i := len(s.queue) - 1; i >= 0 && s.queue[i].kind == commandPublish; i-- {
			if s.queue[i].selection == c.selection {
				s.queueBytes += len(c.text) - len(s.queue[i].text)
				if s.queueBytes > limits.MaxBytes {
					s.queueBytes -= len(c.text) - len(s.queue[i].text)
					s.queueMu.Unlock()
					return false
				}
				s.queue[i] = c
				s.queueMu.Unlock()
				return true
			}
		}
	}
	bytes := len(c.text)
	for _, text := range c.seed {
		bytes += len(text)
	}
	if len(s.queue) >= 64 || s.queueBytes+bytes > limits.MaxBytes {
		s.queueMu.Unlock()
		return false
	}
	s.queue = append(s.queue, c)
	s.queueBytes += bytes
	s.queueMu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return true
}

func (s *Sync) pop() (command, bool) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if len(s.queue) == 0 {
		return command{}, false
	}
	c := s.queue[0]
	s.queueBytes -= len(c.text)
	for _, text := range c.seed {
		s.queueBytes -= len(text)
	}
	copy(s.queue, s.queue[1:])
	s.queue = s.queue[:len(s.queue)-1]
	return c, true
}

func (s *Sync) notifyState() {
	if s.hooks.Status != nil {
		s.hooks.Status(s.State())
	}
}

func (s *Sync) run() {
	var db *sql.DB
	var ticker *time.Ticker
	var tick <-chan time.Time
	watching := false
	last := make(map[byte]int64, 2)
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
		if db != nil {
			db.Close()
		}
		for {
			c, ok := s.pop()
			if !ok {
				return
			}
			if c.reply != nil {
				c.reply("", false)
			}
		}
	}()
	for {
		select {
		case <-s.done:
			return
		case <-s.wake:
			for {
				c, ok := s.pop()
				if !ok {
					break
				}
				switch c.kind {
				case commandEnable:
					if db != nil || !s.requested.Load() {
						continue
					}
					var opened *sql.DB
					var err error
					for attempt := 0; attempt < 5; attempt++ {
						opened, err = open(s.path)
						if err == nil {
							break
						}
						time.Sleep(time.Duration(attempt+1) * 30 * time.Millisecond)
					}
					if err != nil {
						s.requested.Store(false)
						s.available.Store(false)
						s.notifyState()
						continue
					}
					if !s.requested.Load() {
						opened.Close()
						continue
					}
					db = opened
					s.ready.Store(true)
					entries := readAll(db)
					for _, selection := range []byte{'c', 'p'} {
						if entry, ok := entries[selection]; ok {
							last[selection] = entry.Revision
							s.changed(entry)
						} else if text, ok := c.seed[selection]; ok {
							if entry, ok := write(db, s.writer, selection, text, SourceLocal, ""); ok {
								last[selection] = entry.Revision
								s.changed(entry)
							}
						}
					}
					s.notifyState()
					if watching && ticker == nil {
						ticker = time.NewTicker(200 * time.Millisecond)
						tick = ticker.C
					}
				case commandDisable:
					s.ready.Store(false)
					if ticker != nil {
						ticker.Stop()
						ticker, tick = nil, nil
					}
					if db != nil {
						db.Close()
						db = nil
					}
					clear(last)
					s.notifyState()
				case commandPublish:
					if db != nil && s.requested.Load() {
						if entry, ok := write(db, s.writer, c.selection, c.text, c.source, c.display); ok {
							last[c.selection] = entry.Revision
							s.changed(entry)
						}
					}
				case commandQuery:
					if db == nil || !s.requested.Load() {
						c.reply("", false)
					} else if entry, ok := read(db, c.selection); ok {
						changed := entry.Revision != last[c.selection]
						last[c.selection] = entry.Revision
						if changed {
							s.changed(entry)
						}
						c.reply(entry.Text, true)
					} else {
						c.reply("", false)
					}
				case commandWatch:
					watching = c.watch
					if watching && db != nil {
						for selection, entry := range readAll(db) {
							last[selection] = entry.Revision
							s.changed(entry)
						}
					}
					if watching && db != nil && ticker == nil {
						ticker = time.NewTicker(200 * time.Millisecond)
						tick = ticker.C
					} else if !watching && ticker != nil {
						ticker.Stop()
						ticker, tick = nil, nil
					}
				}
			}
		case <-tick:
			if db != nil && s.requested.Load() {
				for selection, revision := range readRevisions(db) {
					if revision != last[selection] {
						if entry, ok := read(db, selection); ok && entry.Revision == revision {
							last[selection] = entry.Revision
							s.changed(entry)
						}
					}
				}
			}
		}
	}
}

func (s *Sync) changed(entry Entry) {
	if s.hooks.Changed != nil {
		s.hooks.Changed(entry)
	}
}

func cacheBootID() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "unknown"
	}
	id := strings.TrimSpace(string(data))
	if len(id) != 36 {
		return "unknown"
	}
	return id
}

func open(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("clipboard cache is disabled")
	}
	if err := secureFile(path); err != nil {
		return nil, err
	}
	params := url.Values{"_busy_timeout": {"250"}, "_journal_mode": {"WAL"}, "_secure_delete": {"on"}, "_synchronous": {"NORMAL"}}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: params.Encode()}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS clipboard_state (
		revision INTEGER PRIMARY KEY AUTOINCREMENT,
		selection INTEGER NOT NULL UNIQUE CHECK (selection IN (99, 112)),
		boot_id TEXT NOT NULL,
		writer_id BLOB NOT NULL CHECK (length(writer_id) = 16),
		source_kind INTEGER NOT NULL,
		source_display TEXT NOT NULL,
		text TEXT NOT NULL
	)`); err == nil {
		_, err = db.Exec(`DELETE FROM clipboard_state WHERE boot_id <> ?`, currentBootID)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func secureFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	var dirStat unix.Stat_t
	if err := unix.Lstat(dir, &dirStat); err != nil {
		return err
	}
	if dirStat.Mode&unix.S_IFMT != unix.S_IFDIR || dirStat.Uid != uint32(os.Getuid()) || dirStat.Mode&0022 != 0 {
		return fmt.Errorf("clipboard cache directory is not private to the current user")
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("clipboard cache is not a current-user regular file")
	}
	return unix.Fchmod(fd, 0600)
}

func readAll(db *sql.DB) map[byte]Entry {
	rows, err := db.Query(`SELECT revision, selection, writer_id, source_kind, source_display, text
		FROM clipboard_state WHERE boot_id = ? AND length(text) <= ? AND length(writer_id) = 16`, currentBootID, limits.ClipboardBytes)
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := make(map[byte]Entry, 2)
	for rows.Next() {
		var entry Entry
		var selection int
		var writer []byte
		var source int
		if rows.Scan(&entry.Revision, &selection, &writer, &source, &entry.Display, &entry.Text) != nil ||
			!validSelection(byte(selection)) || len(writer) != len(entry.Writer) || source < int(SourceLocal) || source > int(SourceXExternal) ||
			len(entry.Display) > 512 || !utf8.ValidString(entry.Display) || len(entry.Text) > limits.ClipboardBytes || !utf8.ValidString(entry.Text) {
			continue
		}
		entry.Selection, entry.Source = byte(selection), Source(source)
		copy(entry.Writer[:], writer)
		result[entry.Selection] = entry
	}
	return result
}

func read(db *sql.DB, selection byte) (Entry, bool) {
	var entry Entry
	var selected int
	var writer []byte
	var source int
	err := db.QueryRow(`SELECT revision, selection, writer_id, source_kind, source_display, text
		FROM clipboard_state WHERE boot_id = ? AND selection = ? AND length(text) <= ? AND length(writer_id) = 16`,
		currentBootID, int(selection), limits.ClipboardBytes).Scan(&entry.Revision, &selected, &writer, &source, &entry.Display, &entry.Text)
	if err != nil || !validSelection(byte(selected)) || len(writer) != len(entry.Writer) || source < int(SourceLocal) || source > int(SourceXExternal) ||
		len(entry.Display) > 512 || !utf8.ValidString(entry.Display) || len(entry.Text) > limits.ClipboardBytes || !utf8.ValidString(entry.Text) {
		return Entry{}, false
	}
	entry.Selection, entry.Source = byte(selected), Source(source)
	copy(entry.Writer[:], writer)
	return entry, true
}

func readRevisions(db *sql.DB) map[byte]int64 {
	rows, err := db.Query(`SELECT selection, revision FROM clipboard_state WHERE boot_id = ?`, currentBootID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := make(map[byte]int64, 2)
	for rows.Next() {
		var selection int
		var revision int64
		if rows.Scan(&selection, &revision) == nil && validSelection(byte(selection)) {
			result[byte(selection)] = revision
		}
	}
	return result
}

func write(db *sql.DB, writer [16]byte, selection byte, text string, source Source, display string) (Entry, bool) {
	if !validSelection(selection) || source > SourceXExternal || len(display) > 512 || !utf8.ValidString(display) || len(text) > limits.ClipboardBytes || !utf8.ValidString(text) {
		return Entry{}, false
	}
	for attempt := 0; attempt < 3; attempt++ {
		tx, err := db.Begin()
		if err == nil {
			_, err = tx.Exec(`DELETE FROM clipboard_state WHERE selection = ?`, int(selection))
		}
		var result sql.Result
		if err == nil {
			result, err = tx.Exec(`INSERT INTO clipboard_state
				(selection, boot_id, writer_id, source_kind, source_display, text) VALUES (?, ?, ?, ?, ?, ?)`,
				int(selection), currentBootID, writer[:], int(source), display, text)
		}
		if err == nil {
			err = tx.Commit()
		} else if tx != nil {
			tx.Rollback()
		}
		if err == nil {
			revision, idErr := result.LastInsertId()
			return Entry{Selection: selection, Text: text, Revision: revision, Writer: writer, Source: source, Display: display}, idErr == nil
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	return Entry{}, false
}
