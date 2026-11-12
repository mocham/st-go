package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func diskEditor(t *testing.T, text string, options journalOptions) (*Editor, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "document.txt")
	if err := os.WriteFile(path, []byte(text), 0640); err != nil {
		t.Fatal(err)
	}
	e := newEditor(options)
	if _, err := e.open(path, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.close(false) })
	return e, path
}

func TestJournalRecoversEveryEditingOperationAndUndo(t *testing.T) {
	options := defaultJournalOptions()
	e, path := diskEditor(t, "one two\nthree four\n", options)
	for _, keys := range []string{"cwα<Esc>", "jdd", "u", "<C-r>", "u", "ggA!<Esc>", "vlyp", "<C-v>jIX<Esc>"} {
		editKeys(e, keys)
	}
	want := editorText(e)
	swap := e.buffer().journal.path
	if err := e.buffer().journal.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := e.buffer().journal.Close(false); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "one two\nthree four\n" {
		t.Fatal("editing changed original without write")
	}
	recovered := newEditor(options)
	b, err := recovered.open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.close(false)
	if got := editorText(recovered); got != want {
		t.Fatalf("recovery=%q want%q", got, want)
	}
	if !b.dirty() || !b.recovered || b.journal.path != swap {
		t.Fatal("recovery state lost")
	}
	editKeys(recovered, "u")
	editKeys(e, "u") // The closed original journal must refuse further mutation.
	if !strings.Contains(e.message, "not writable") {
		t.Fatalf("closed journal edit not rejected: %s", e.message)
	}
	if len(b.redo) == 0 {
		t.Fatal("undo history was not reconstructed")
	}
	editKeys(recovered, "<C-r>")
	if editorText(recovered) != want {
		t.Fatal("redo after recovery changed data")
	}
}

func TestJournalIntervalThresholdAndNoIdleWrites(t *testing.T) {
	options := defaultJournalOptions()
	options.Bytes = 1 << 20
	options.Interval = time.Hour
	e, _ := diskEditor(t, "a\n", options)
	j := e.buffer().journal
	editKeys(e, "iabc<Esc>")
	if j.unsynced == 0 {
		t.Fatal("edit was not journaled")
	}
	if err := j.Tick(j.lastAttempt.Add(options.Interval)); err != nil {
		t.Fatal(err)
	}
	if j.unsynced != 0 {
		t.Fatal("time interval did not flush")
	}
	before, _ := j.file.Stat()
	last := j.lastAttempt
	if err := j.Tick(last.Add(options.Interval * 2)); err != nil {
		t.Fatal(err)
	}
	after, _ := j.file.Stat()
	if j.lastAttempt != last || before.Size() != after.Size() {
		t.Fatal("idle journal wrote or synced without changes")
	}
	j.options.Bytes = 1
	editKeys(e, "Ax<Esc>")
	if j.unsynced != 0 {
		t.Fatal("size threshold did not flush")
	}
}

func TestJournalTornTailAndCorruptionPreserveOriginal(t *testing.T) {
	for _, kind := range []string{"header", "payload", "checksum", "huge-length"} {
		t.Run(kind, func(t *testing.T) {
			e, path := diskEditor(t, "original\n", defaultJournalOptions())
			editKeys(e, "A saved<Esc>")
			j := e.buffer().journal
			if err := j.Sync(); err != nil {
				t.Fatal(err)
			}
			if err := j.Close(false); err != nil {
				t.Fatal(err)
			}
			good, err := os.ReadFile(j.path)
			if err != nil {
				t.Fatal(err)
			}
			tail := []byte{1, 2, 3}
			switch kind {
			case "payload":
				tail = make([]byte, 10)
				binary.LittleEndian.PutUint32(tail, 100)
			case "checksum":
				tail = make([]byte, 10)
				binary.LittleEndian.PutUint32(tail, 2)
				copy(tail[8:], "{}")
			case "huge-length":
				tail = make([]byte, 8)
				binary.LittleEndian.PutUint32(tail, uint32(maxRecordBytes))
			}
			corrupt := append(append([]byte(nil), good...), tail...)
			if err := os.WriteFile(j.path, corrupt, 0600); err != nil {
				t.Fatal(err)
			}
			r := newEditor(defaultJournalOptions())
			b, err := r.open(path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer r.close(false)
			if editorText(r) != "original saved\n" || !strings.Contains(b.warning, "preserved") {
				t.Fatalf("recovered=%q warning=%q", editorText(r), b.warning)
			}
			backups, _ := filepath.Glob(j.path + ".damaged-*")
			if len(backups) != 1 {
				t.Fatalf("damaged backup missing: %v", backups)
			}
			data, err := os.ReadFile(backups[0])
			if err != nil || !bytes.Equal(data, corrupt) {
				t.Fatal("damaged journal was not preserved exactly")
			}
			editKeys(r, "A again<Esc>")
			if err := b.journal.Sync(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJournalLocksExistingSwapsSymlinksAndPermissions(t *testing.T) {
	e, path := diskEditor(t, "a\n", defaultJournalOptions())
	j := e.buffer().journal
	other := newEditor(defaultJournalOptions())
	defer other.close(true)
	if b, err := other.open(path, false); err != nil || !b.readonly || b.journal != nil {
		t.Fatalf("existing swap must open readonly without owning a journal: %v", err)
	}
	recovery := newEditor(defaultJournalOptions())
	defer recovery.close(false)
	if _, err := recovery.open(path, true); err == nil {
		t.Fatal("active swap lock bypassed")
	}
	link := filepath.Join(filepath.Dir(path), "link.txt")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.open(link, true); err == nil {
		t.Fatal("symlink opened the same file concurrently")
	}
	info, err := os.Stat(j.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("swap permissions are not private")
	}
	if err := j.Close(false); err != nil {
		t.Fatal(err)
	}
	os.Chmod(j.path, 0644)
	if _, err := recovery.open(path, true); err == nil {
		t.Fatal("unsafe swap permissions accepted")
	}
}

func TestAtomicSaveConflictAndJournalSavedState(t *testing.T) {
	e, path := diskEditor(t, "a\n", defaultJournalOptions())
	editKeys(e, "Aα<Esc>")
	b := e.buffer()
	if err := os.WriteFile(path, []byte("external\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := b.save(false); err == nil {
		t.Fatal("external edit overwritten silently")
	}
	if data, _ := os.ReadFile(path); string(data) != "external\n" {
		t.Fatal("failed save altered file")
	}
	if err := b.save(true); err != nil {
		t.Fatal(err)
	}
	if b.dirty() {
		t.Fatal("successful save stayed modified")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatal("file permissions changed")
	}
	if err := b.journal.Close(false); err != nil {
		t.Fatal(err)
	}
	r := newEditor(defaultJournalOptions())
	rb, err := r.open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close(false)
	if rb.dirty() || editorText(r) != "aα\n" {
		t.Fatal("saved checkpoint was not recovered")
	}
	editKeys(r, "u")
	if editorText(r) != "a\n" || !rb.dirty() {
		t.Fatal("undo across saved checkpoint failed")
	}
}

func TestFileEncodingsEndingsAndSwapFallback(t *testing.T) {
	for _, kind := range []string{"utf8", "bom", "gb18030", "crlf", "no-eol", "empty"} {
		t.Run(kind, func(t *testing.T) {
			text := "café 世界\n"
			data := []byte(text)
			switch kind {
			case "bom":
				data = append([]byte{0xef, 0xbb, 0xbf}, data...)
			case "gb18030":
				var err error
				data, err = simplifiedchinese.GB18030.NewEncoder().Bytes(data)
				if err != nil {
					t.Fatal(err)
				}
			case "crlf":
				data = []byte("café 世界\r\n")
			case "no-eol":
				data = []byte("café 世界")
			case "empty":
				data = nil
			}
			path := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			b, err := loadBuffer(path)
			if err != nil {
				t.Fatal(err)
			}
			roundtrip, err := b.encoded()
			if err != nil || !bytes.Equal(roundtrip, data) {
				t.Fatalf("lossy roundtrip %q -> %q, %v", data, roundtrip, err)
			}
			options := defaultJournalOptions()
			options.Directory = t.TempDir()
			if err := attachJournal(b, options, false); err != nil {
				t.Fatal(err)
			}
			defer b.journal.Close(true)
			if filepath.Dir(b.journal.path) != options.Directory {
				t.Fatal("explicit swap directory ignored")
			}
		})
	}
}

func TestWriteFailureDoesNotApplyUnjournaledEdit(t *testing.T) {
	e, _ := diskEditor(t, "abc\n", defaultJournalOptions())
	e.buffer().journal.file.Close()
	editKeys(e, "x")
	if editorText(e) != "abc\n" || !e.buffer().journal.broken {
		t.Fatal("edit applied after failed journal write")
	}
}
