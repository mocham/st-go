package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const journalMagic = "GOVIMSWP\x00v1\n"
const maxRecordBytes = 128 << 20

var journalCRC = crc32.MakeTable(crc32.Castagnoli)

type journalRecord struct {
	Seq          uint64          `json:"seq"`
	Kind         string          `json:"kind"`
	Meta         *fileMetadata   `json:"meta,omitempty"`
	Text         string          `json:"text,omitempty"`
	Version      uint64          `json:"version"`
	SavedVersion uint64          `json:"saved_version,omitempty"`
	Group        uint64          `json:"group,omitempty"`
	Before       int             `json:"before,omitempty"`
	Cursor       int             `json:"cursor,omitempty"`
	Edits        []patch         `json:"edits,omitempty"`
	PID          int             `json:"pid,omitempty"`
	Host         string          `json:"host,omitempty"`
	Created      int64           `json:"created,omitempty"`
	Placeholder  bool            `json:"placeholder,omitempty"`
	History      *journalHistory `json:"history,omitempty"`
}

type journalHistory struct {
	Undo, Redo             []historyEntry
	NextVersion, NextGroup uint64
}

type journalOptions struct {
	Directory string
	Interval  time.Duration
	Bytes     int
}

func defaultJournalOptions() journalOptions {
	return journalOptions{Interval: time.Second, Bytes: 64 << 10}
}

// Only normal file opens may fall back to readonly. Recovery and renaming a
// writable buffer must still reject an existing journal.
type swapExistsError struct{ path string }

func (e *swapExistsError) Error() string {
	return fmt.Sprintf("swap exists: %s (it is never overwritten)", e.path)
}

type Journal struct {
	file           *os.File
	path           string
	seq            uint64
	options        journalOptions
	unsynced       int
	lastAttempt    time.Time
	broken, closed bool
	syncError      error
	syncFile       func() error
}

func journalPaths(path string, options journalOptions) ([]string, error) {
	directory := options.Directory
	if directory == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			if path != "" {
				return []string{filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".govim.swp")}, nil
			}
			return nil, err
		}
		directory = filepath.Join(cache, "govim", "swap")
	}
	if path == "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, err
		}
		f, err := os.CreateTemp(directory, "untitled-*.govim.swp")
		if err != nil {
			return nil, err
		}
		name := f.Name()
		f.Close()
		os.Remove(name)
		return []string{name}, nil
	}
	fallback := filepath.Join(directory, digest([]byte(path))+".govim.swp")
	if options.Directory != "" {
		return []string{fallback}, nil
	}
	return []string{filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".govim.swp"), fallback}, nil
}

func attachJournal(b *Buffer, options journalOptions, recoverFile bool) error {
	paths, err := journalPaths(b.meta.Path, options)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			if !recoverFile {
				return &swapExistsError{path: path}
			}
			return recoverJournal(b, path, options)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if recoverFile {
		return fmt.Errorf("no govim swap file found for %s", b.meta.Path)
	}
	var failures []string
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		j, err := openJournal(path, true, options)
		if err != nil {
			if os.IsExist(err) {
				return &swapExistsError{path: path}
			}
			failures = append(failures, err.Error())
			continue
		}
		b.journal = j
		host, _ := os.Hostname()
		record := journalRecord{Kind: "snapshot", Text: string(b.text), Meta: &b.meta, Version: b.version, SavedVersion: b.savedVersion, PID: os.Getpid(), Host: host, Created: time.Now().Unix(), Placeholder: b.placeholder}
		if len(b.undo) > 0 || len(b.redo) > 0 {
			record.History = &journalHistory{b.undo, b.redo, b.nextVersion, b.nextGroup}
		}
		_, err = j.Append(record)
		if err == nil {
			err = j.Sync()
		}
		if err == nil {
			err = syncDirectory(filepath.Dir(path))
		}
		if err != nil {
			j.Close(false)
			b.journal = nil
			return err
		}
		return nil
	}
	return fmt.Errorf("cannot create a durable swap file: %s", strings.Join(failures, "; "))
}

func openJournal(path string, create bool, options journalOptions) (*Journal, error) {
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if create {
		flags |= unix.O_CREAT | unix.O_EXCL
	}
	fd, err := unix.Open(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	fail := func(err error) (*Journal, error) { f.Close(); return nil, err }
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || info.Mode().Perm()&0077 != 0 {
		return fail(fmt.Errorf("unsafe swap file ownership, type, links or permissions: %s", path))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fail(fmt.Errorf("swap is in use by another editor: %s", path))
	}
	j := &Journal{file: f, path: path, options: options, lastAttempt: time.Now()}
	if create {
		if err := writeAll(f, []byte(journalMagic)); err != nil {
			return fail(err)
		}
	}
	return j, nil
}

func (j *Journal) Append(record journalRecord) (bool, error) {
	if j.closed || j.broken {
		return false, fmt.Errorf("swap journal is not writable; keep it for recovery")
	}
	if j.syncError != nil {
		return false, fmt.Errorf("swap sync failed; :preserve retries it: %w", j.syncError)
	}
	record.Seq = j.seq + 1
	estimate := 4096 + len(record.Edits)*128 + jsonTextSize(record.Text)
	if record.Meta != nil {
		estimate += jsonTextSize(record.Meta.Path)
	}
	for _, p := range record.Edits {
		estimate += jsonTextSize(p.Old) + jsonTextSize(p.New)
	}
	if record.History != nil {
		for _, stack := range [][]historyEntry{record.History.Undo, record.History.Redo} {
			for _, entry := range stack {
				estimate += 512
				for _, p := range entry.Edits {
					estimate += 128 + jsonTextSize(p.Old) + jsonTextSize(p.New)
				}
			}
		}
	}
	if estimate > maxRecordBytes {
		return false, fmt.Errorf("journal record exceeds size limit")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	if len(data) > maxRecordBytes {
		return false, fmt.Errorf("journal record exceeds size limit")
	}
	var header [8]byte
	binary.LittleEndian.PutUint32(header[:4], uint32(len(data)))
	binary.LittleEndian.PutUint32(header[4:], crc32.Checksum(data, journalCRC))
	if err := writeAll(j.file, header[:]); err != nil {
		j.broken = true
		return false, err
	}
	if err := writeAll(j.file, data); err != nil {
		j.broken = true
		return false, err
	}
	j.seq++
	j.unsynced += len(header) + len(data)
	if j.unsynced >= j.options.Bytes || time.Since(j.lastAttempt) >= j.options.Interval {
		return true, j.Sync()
	}
	return true, nil
}

func (j *Journal) Sync() error {
	if j.closed {
		return fmt.Errorf("swap journal is closed")
	}
	if j.broken {
		return fmt.Errorf("swap journal has a partial write; recover before editing further")
	}
	if j.unsynced == 0 && j.syncError == nil {
		return nil
	}
	j.lastAttempt = time.Now()
	var err error
	if j.syncFile != nil {
		err = j.syncFile()
	} else {
		err = j.file.Sync()
	}
	j.syncError = err
	if err == nil {
		j.unsynced = 0
	}
	return err
}
func (j *Journal) Tick(now time.Time) error {
	if j.unsynced > 0 && now.Sub(j.lastAttempt) >= j.options.Interval {
		return j.Sync()
	}
	return nil
}

func (j *Journal) Close(discard bool) error {
	if j == nil || j.closed {
		return nil
	}
	err := j.Sync()
	if discard && err == nil {
		opened, e1 := j.file.Stat()
		current, e2 := os.Lstat(j.path)
		if e1 == nil && e2 == nil && os.SameFile(opened, current) {
			if e := os.Remove(j.path); e != nil {
				err = e
			} else {
				err = syncDirectory(filepath.Dir(j.path))
			}
		}
	}
	j.closed = true
	closeErr := j.file.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func recoverJournal(b *Buffer, path string, options journalOptions) error {
	j, err := openJournal(path, false, options)
	if err != nil {
		return err
	}
	failed := true
	defer func() {
		if failed {
			j.file.Close()
		}
	}()
	magic := make([]byte, len(journalMagic))
	if _, err := io.ReadFull(j.file, magic); err != nil || string(magic) != journalMagic {
		return fmt.Errorf("not a govim swap file: %s", path)
	}
	var recovered *Buffer
	validEnd := int64(len(magic))
	info, err := j.file.Stat()
	if err != nil {
		return err
	}
	var warning string
	cursor := 0
	for {
		var header [8]byte
		n, err := io.ReadFull(j.file, header[:])
		if err == io.EOF && n == 0 {
			break
		}
		if err != nil {
			warning = "incomplete final record"
			break
		}
		length := int(binary.LittleEndian.Uint32(header[:4]))
		if length < 1 || length > maxRecordBytes {
			warning = "invalid record length"
			break
		}
		if int64(length) > info.Size()-validEnd-8 {
			warning = "incomplete final record"
			break
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(j.file, data); err != nil {
			warning = "incomplete final record"
			break
		}
		if crc32.Checksum(data, journalCRC) != binary.LittleEndian.Uint32(header[4:]) {
			warning = "record checksum mismatch"
			break
		}
		var record journalRecord
		if err := json.Unmarshal(data, &record); err != nil || record.Seq != j.seq+1 {
			warning = "invalid journal record or sequence"
			break
		}
		if recovered == nil {
			if record.Kind != "snapshot" || record.Meta == nil || len(record.Text) > maxBufferBytes || strings.Count(record.Text, "\n") >= maxLines || record.Meta.Path != b.meta.Path {
				return fmt.Errorf("swap snapshot does not match requested file: %s", path)
			}
			if record.Meta.Encoding != "utf-8" && record.Meta.Encoding != "gb18030" {
				return fmt.Errorf("unsupported encoding in swap")
			}
			recovered = &Buffer{meta: *record.Meta, text: []rune(record.Text), byteSize: len(record.Text), version: record.Version, nextVersion: record.Version, savedVersion: record.SavedVersion, placeholder: record.Placeholder}
			if record.History != nil {
				history := record.History
				size := 0
				for _, stack := range [][]historyEntry{history.Undo, history.Redo} {
					for _, entry := range stack {
						if entry.ID == 0 || len(entry.Edits) > maxPatches {
							return fmt.Errorf("invalid snapshot history")
						}
						size += historySize(entry.Edits)
					}
				}
				if size > maxHistoryBytes+4096 {
					return fmt.Errorf("snapshot history exceeds limit")
				}
				recovered.undo, recovered.redo = history.Undo, history.Redo
				recovered.nextVersion = max(recovered.nextVersion, history.NextVersion)
				recovered.nextGroup = history.NextGroup
			}
		} else {
			switch record.Kind {
			case "edit", "undo", "redo":
				if record.Kind == "edit" && (record.Group == 0 || record.Version <= recovered.nextVersion) {
					warning = "invalid edit version/group"
					break
				}
				if record.Kind == "undo" && (len(recovered.undo) == 0 || recovered.undo[len(recovered.undo)-1].ID != record.Group) {
					warning = "undo history mismatch"
					break
				}
				if record.Kind == "redo" && (len(recovered.redo) == 0 || recovered.redo[len(recovered.redo)-1].ID != record.Group) {
					warning = "redo history mismatch"
					break
				}
				result, err := validatePatches(recovered.text, record.Edits)
				if err != nil {
					warning = err.Error()
					break
				}
				recovered.apply(record, result)
				cursor = record.Cursor
			case "saved":
				if record.Meta == nil || record.Meta.Path != recovered.meta.Path || record.Version != recovered.version || (record.Meta.Encoding != "utf-8" && record.Meta.Encoding != "gb18030") {
					warning = "invalid saved-file record"
					break
				}
				recovered.meta = *record.Meta
				recovered.savedVersion = record.Version
			default:
				warning = "unknown journal record"
			}
			if warning != "" {
				break
			}
		}
		j.seq = record.Seq
		validEnd += int64(8 + length)
	}
	if recovered == nil {
		return fmt.Errorf("no complete recovery snapshot in %s", path)
	}
	if warning != "" {
		backup, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".damaged-*")
		if err != nil {
			return err
		}
		backupName := backup.Name()
		if _, err = j.file.Seek(0, io.SeekStart); err == nil {
			_, err = io.Copy(backup, j.file)
		}
		if err == nil {
			err = backup.Sync()
		}
		closeErr := backup.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = syncDirectory(filepath.Dir(path))
		}
		if err != nil {
			return fmt.Errorf("preserve damaged journal: %w", err)
		}
		if err = j.file.Truncate(validEnd); err != nil {
			return err
		}
		if err = j.file.Sync(); err != nil {
			return err
		}
		recovered.warning = "Recovered valid prefix (" + warning + "); original journal preserved as " + backupName
	}
	if _, err = j.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	recovered.journal, recovered.recovered = j, true
	if recovered.warning == "" {
		recovered.warning = fmt.Sprintf("Recovered %s (%d journal records); review and :w", path, j.seq)
	}
	// The snapshot and operations are self-contained. Never overwrite an
	// externally changed original without an explicit :w! after review.
	if current, err := readBounded(recovered.meta.Path, maxBufferBytes*4); err == nil && digest(current) != recovered.meta.DiskHash {
		recovered.warning += " [original changed on disk]"
	}
	*b = *recovered
	// Cursor is a recovery hint, separate from undo history.
	b.recoveryCursor = cursor
	failed = false
	return nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func jsonTextSize(text string) int {
	size := 2
	for _, r := range text {
		switch {
		case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t' || r == '\b' || r == '\f':
			size += 2
		case r < 0x20 || r == '<' || r == '>' || r == '&' || r == 0x2028 || r == 0x2029:
			size += 6
		default:
			if r < 0x80 {
				size++
			} else if r < 0x800 {
				size += 2
			} else if r < 0x10000 {
				size += 3
			} else {
				size += 4
			}
		}
	}
	return size
}
