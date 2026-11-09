package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const maxBufferBytes = 16 << 20
const maxHistoryBytes = 64 << 20
const maxCount = 1000000
const maxLines = 1000000
const maxPatches = 65536

type patch struct {
	At  int    `json:"at"`
	Old string `json:"old"`
	New string `json:"new"`
}

type historyEntry struct {
	ID                          uint64
	Before, After               int
	BeforeVersion, AfterVersion uint64
	Edits                       []patch
	BeforeEmpty, AfterEmpty     bool
}

type fileMetadata struct {
	Path     string `json:"path"`
	Encoding string `json:"encoding"`
	CRLF     bool   `json:"crlf,omitempty"`
	BOM      bool   `json:"bom,omitempty"`
	FinalEOL bool   `json:"final_eol,omitempty"`
	Existed  bool   `json:"existed,omitempty"`
	DiskHash string `json:"disk_hash"`
	Mode     uint32 `json:"mode"`
}

type Buffer struct {
	meta                                          fileMetadata
	text                                          []rune // logical lines, without the file's final line terminator
	lines                                         []int
	byteSize                                      int
	version, savedVersion, nextVersion, nextGroup uint64
	undo, redo                                    []historyEntry
	journal                                       *Journal
	readonly                                      bool
	recovered                                     bool
	warning                                       string
	recoveryCursor                                int
	placeholder                                   bool
	syntax                                        syntaxCache
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	// The stat below refuses non-regular files. Nonblocking open also prevents
	// a preview include (or a raced replacement) that is a FIFO from hanging
	// before we get a chance to perform that check.
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file exceeds %d MiB", limit>>20)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds size limit")
	}
	return data, nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func loadBuffer(path string) (*Buffer, error) {
	b := &Buffer{meta: fileMetadata{Encoding: "utf-8", FinalEOL: true, Mode: 0600}, placeholder: true}
	if path == "" {
		return b, nil
	}
	name, err := canonicalPath(path)
	if err != nil {
		return nil, err
	}
	b.meta.Path = name
	if !utf8.ValidString(name) {
		return nil, fmt.Errorf("file name is not valid UTF-8")
	}
	data, err := readBounded(name, maxBufferBytes)
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	b.meta.Existed, b.meta.Mode, b.meta.DiskHash = true, uint32(info.Mode().Perm()), digest(data)
	decoded := data
	b.placeholder = len(data) == 0
	if len(data) >= 3 && string(data[:3]) == "\xef\xbb\xbf" {
		b.meta.BOM = true
		decoded = data[3:]
	}
	if !utf8.Valid(decoded) {
		if b.meta.BOM {
			return nil, fmt.Errorf("invalid UTF-8 after BOM")
		}
		decoded, err = simplifiedchinese.GB18030.NewDecoder().Bytes(data)
		if err != nil {
			return nil, fmt.Errorf("decode GB18030: %w", err)
		}
		roundtrip, err := simplifiedchinese.GB18030.NewEncoder().Bytes(decoded)
		if err != nil || string(roundtrip) != string(data) {
			return nil, fmt.Errorf("file is neither valid UTF-8 nor losslessly decodable GB18030")
		}
		b.meta.Encoding = "gb18030"
	}
	if len(decoded) > maxBufferBytes {
		return nil, fmt.Errorf("decoded file exceeds size limit")
	}
	s := string(decoded)
	if strings.Contains(s, "\r\n") && strings.Count(s, "\r\n") == strings.Count(s, "\n") {
		b.meta.CRLF = true
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	b.meta.FinalEOL = strings.HasSuffix(s, "\n")
	if b.meta.FinalEOL {
		s = strings.TrimSuffix(s, "\n")
	}
	b.text = []rune(s)
	if strings.Count(s, "\n") >= maxLines {
		return nil, fmt.Errorf("file exceeds line limit")
	}
	b.byteSize = len(s)
	return b, nil
}

func (b *Buffer) encoded() ([]byte, error) {
	if b.placeholder && len(b.text) == 0 {
		return nil, nil
	}
	s := string(b.text)
	if b.meta.FinalEOL {
		s += "\n"
	}
	if b.meta.CRLF {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	data := []byte(s)
	if b.meta.Encoding == "gb18030" {
		var err error
		data, err = simplifiedchinese.GB18030.NewEncoder().Bytes(data)
		if err != nil {
			return nil, err
		}
	}
	if b.meta.BOM {
		data = append([]byte{0xef, 0xbb, 0xbf}, data...)
	}
	return data, nil
}

func (b *Buffer) lineStarts() []int {
	if b.lines == nil {
		b.lines = []int{0}
		for i, r := range b.text {
			if r == '\n' {
				b.lines = append(b.lines, i+1)
			}
		}
	}
	return b.lines
}
func (b *Buffer) row(pos int) int {
	pos = clamp(pos, 0, len(b.text))
	lines := b.lineStarts()
	lo, hi := 0, len(lines)
	for lo < hi {
		mid := (lo + hi) / 2
		if lines[mid] <= pos {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return max(0, lo-1)
}
func (b *Buffer) start(row int) int {
	lines := b.lineStarts()
	return lines[clamp(row, 0, len(lines)-1)]
}
func (b *Buffer) end(row int) int {
	lines := b.lineStarts()
	row = clamp(row, 0, len(lines)-1)
	if row+1 < len(lines) {
		return lines[row+1] - 1
	}
	return len(b.text)
}
func (b *Buffer) line(row int) []rune { return b.text[b.start(row):b.end(row)] }
func (b *Buffer) normal(pos int) int {
	pos = clamp(pos, 0, len(b.text))
	row := b.row(pos)
	return clamp(pos, b.start(row), max(b.start(row), b.end(row)-1))
}
func (b *Buffer) dirty() bool   { return b.version != b.savedVersion }
func (b *Buffer) group() uint64 { b.nextGroup++; return b.nextGroup }

func validatePatches(text []rune, edits []patch) ([]rune, error) {
	if len(edits) > maxPatches {
		return nil, fmt.Errorf("too many patches in one operation")
	}
	// Bulk operators emit descending, disjoint ranges. Validate all against the
	// original and build once, rather than copying a large buffer per match.
	if disjointDescending(edits) {
		size, lines := len(string(text)), 1
		for _, r := range text {
			if r == '\n' {
				lines++
			}
		}
		for _, p := range edits {
			old := []rune(p.Old)
			if !utf8.ValidString(p.New) || !utf8.ValidString(p.Old) || p.At < 0 || p.At > len(text) || len(old) > len(text)-p.At || string(text[p.At:p.At+len(old)]) != p.Old {
				return nil, fmt.Errorf("edit does not match buffer")
			}
			size += len(p.New) - len(p.Old)
			lines += strings.Count(p.New, "\n") - strings.Count(p.Old, "\n")
			if size < 0 || size > maxBufferBytes || lines > maxLines {
				return nil, fmt.Errorf("edit exceeds buffer/line limit")
			}
		}
		result := make([]rune, 0, len(text))
		at := 0
		for i := len(edits) - 1; i >= 0; i-- {
			p := edits[i]
			result = append(result, text[at:p.At]...)
			result = append(result, []rune(p.New)...)
			at = p.At + utf8.RuneCountInString(p.Old)
		}
		result = append(result, text[at:]...)
		return result, nil
	}
	result := append([]rune(nil), text...)
	size := len(string(result))
	lines := strings.Count(string(result), "\n") + 1
	for _, p := range edits {
		if !utf8.ValidString(p.New) || !utf8.ValidString(p.Old) {
			return nil, fmt.Errorf("invalid UTF-8 edit")
		}
		old := []rune(p.Old)
		if p.At < 0 || p.At > len(result) || len(old) > len(result)-p.At || string(result[p.At:p.At+len(old)]) != p.Old {
			return nil, fmt.Errorf("edit does not match buffer")
		}
		size += len(p.New) - len(p.Old)
		lines += strings.Count(p.New, "\n") - strings.Count(p.Old, "\n")
		if lines > maxLines {
			return nil, fmt.Errorf("edit exceeds line limit")
		}
		if size < 0 || size > maxBufferBytes {
			return nil, fmt.Errorf("edit exceeds %d MiB buffer limit", maxBufferBytes>>20)
		}
		insert := []rune(p.New)
		next := make([]rune, 0, len(result)-len(old)+len(insert))
		next = append(next, result[:p.At]...)
		next = append(next, insert...)
		next = append(next, result[p.At+len(old):]...)
		result = next
	}
	return result, nil
}

func (b *Buffer) change(edits []patch, before, after int, group uint64, empty ...bool) (bool, error) {
	if b.readonly {
		return false, fmt.Errorf("readonly buffer")
	}
	if len(edits) == 0 {
		return false, nil
	}
	result, err := validatePatches(b.text, edits)
	if err != nil {
		return false, err
	}
	placeholder := false
	if len(empty) > 0 {
		placeholder = empty[0]
	}
	if len(result) > 0 {
		placeholder = false
	}
	if string(result) == string(b.text) && placeholder == b.placeholder {
		return false, nil
	}
	if group == 0 {
		group = b.group()
	}
	var prospective []patch
	if len(b.undo) > 0 && b.undo[len(b.undo)-1].ID == group {
		prospective = append(prospective, b.undo[len(b.undo)-1].Edits...)
	}
	prospective = mergeHistory(prospective, edits)
	if historySize(prospective) > maxHistoryBytes {
		return false, fmt.Errorf("undo group exceeds memory limit; end this command/insert session first")
	}
	if len(prospective) > maxPatches {
		return false, fmt.Errorf("undo group has too many edits; end this insert session first")
	}
	record := journalRecord{Kind: "edit", Group: group, Before: before, Cursor: after, Version: b.nextVersion + 1, Edits: edits, Placeholder: placeholder}
	committed, err := b.append(record)
	if committed {
		b.apply(record, result)
	}
	return committed, err
}

func (b *Buffer) append(record journalRecord) (bool, error) {
	if b.journal == nil {
		return true, nil
	}
	return b.journal.Append(record)
}

func (b *Buffer) apply(record journalRecord, result []rune) {
	b.invalidateSyntax(record)
	previous := b.version
	previousEmpty := b.placeholder
	b.placeholder = record.Placeholder
	b.text, b.lines, b.byteSize = result, nil, len(string(result))
	b.version = record.Version
	b.nextVersion = max(b.nextVersion, record.Version)
	b.nextGroup = max(b.nextGroup, record.Group)
	switch record.Kind {
	case "edit":
		b.redo = nil
		if len(b.undo) == 0 || b.undo[len(b.undo)-1].ID != record.Group {
			b.undo = append(b.undo, historyEntry{ID: record.Group, Before: record.Before, BeforeVersion: previous, BeforeEmpty: previousEmpty})
		}
		h := &b.undo[len(b.undo)-1]
		h.Edits = mergeHistory(h.Edits, record.Edits)
		h.After, h.AfterVersion = record.Cursor, record.Version
		h.AfterEmpty = b.placeholder
	case "undo":
		if len(b.undo) > 0 && b.undo[len(b.undo)-1].ID == record.Group {
			b.redo = append(b.redo, b.undo[len(b.undo)-1])
			b.undo = b.undo[:len(b.undo)-1]
		}
	case "redo":
		if len(b.redo) > 0 && b.redo[len(b.redo)-1].ID == record.Group {
			b.undo = append(b.undo, b.redo[len(b.redo)-1])
			b.redo = b.redo[:len(b.redo)-1]
		}
	}
	b.trimHistory()
}

func (b *Buffer) trimHistory() {
	size := 0
	for i := len(b.undo) - 1; i >= 0; i-- {
		size += historySize(b.undo[i].Edits)
		if size > maxHistoryBytes && i < len(b.undo)-1 {
			b.undo = append([]historyEntry(nil), b.undo[i+1:]...)
			break
		}
	}
}

func (b *Buffer) undoChange(redo bool) (int, error) {
	if b.readonly {
		return -1, fmt.Errorf("readonly buffer")
	}
	stack := b.undo
	if redo {
		stack = b.redo
	}
	if len(stack) == 0 {
		return -1, fmt.Errorf("already at %s change", map[bool]string{false: "oldest", true: "newest"}[redo])
	}
	h := stack[len(stack)-1]
	record := journalRecord{Kind: "undo", Group: h.ID, Cursor: h.Before, Version: h.BeforeVersion, Placeholder: h.BeforeEmpty}
	if redo {
		record.Kind, record.Cursor, record.Version, record.Edits = "redo", h.After, h.AfterVersion, h.Edits
		record.Placeholder = h.AfterEmpty
	} else {
		record.Edits = inversePatches(h.Edits)
	}
	result, err := validatePatches(b.text, record.Edits)
	if err != nil {
		return -1, err
	}
	committed, err := b.append(record)
	if committed {
		b.apply(record, result)
		return b.normal(record.Cursor), err
	}
	return -1, err
}

func disjointDescending(edits []patch) bool {
	for i, p := range edits {
		if i > 0 && (p.At >= edits[i-1].At || p.At+utf8.RuneCountInString(p.Old) > edits[i-1].At) {
			return false
		}
	}
	return true
}
func inversePatches(edits []patch) []patch {
	result := make([]patch, 0, len(edits))
	if disjointDescending(edits) {
		shift := 0
		for i := len(edits) - 1; i >= 0; i-- {
			p := edits[i]
			result = append(result, patch{p.At + shift, p.New, p.Old})
			shift += utf8.RuneCountInString(p.New) - utf8.RuneCountInString(p.Old)
		}
		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}
		return result
	}
	for i := len(edits) - 1; i >= 0; i-- {
		p := edits[i]
		result = append(result, patch{p.At, p.New, p.Old})
	}
	return result
}

func historySize(edits []patch) int {
	size := 0
	for _, p := range edits {
		size += len(p.Old) + len(p.New) + 64
	}
	return size
}
func mergeHistory(previous, edits []patch) []patch {
	for _, p := range edits {
		if len(previous) > 0 {
			last := &previous[len(previous)-1]
			text := []rune(last.New)
			offset := p.At - last.At
			old := []rune(p.Old)
			if offset >= 0 && offset <= len(text) && len(old) <= len(text)-offset && string(text[offset:offset+len(old)]) == p.Old {
				last.New = string(text[:offset]) + p.New + string(text[offset+len(old):])
				continue
			}
			if p.At == last.At+len(text) {
				last.Old += p.Old
				last.New += p.New
				continue
			}
		}
		previous = append(previous, p)
	}
	return previous
}

func (b *Buffer) save(force bool) error {
	if b.meta.Path == "" {
		return fmt.Errorf("no file name; use :w FILE")
	}
	if b.readonly && !force {
		return fmt.Errorf("readonly buffer (use :w!)")
	}
	if b.journal != nil {
		if err := b.journal.Sync(); err != nil {
			return fmt.Errorf("cannot preserve edits before save: %w", err)
		}
	}
	current, err := readBounded(b.meta.Path, maxBufferBytes*4)
	if err != nil && !os.IsNotExist(err) && !force {
		return err
	}
	if !force && ((err == nil && (!b.meta.Existed || digest(current) != b.meta.DiskHash)) || (os.IsNotExist(err) && b.meta.Existed)) {
		return fmt.Errorf("file changed on disk; use :w! only to overwrite deliberately")
	}
	data, err := b.encoded()
	if err != nil {
		return err
	}
	replaced, err := atomicWrite(b.meta.Path, data, os.FileMode(b.meta.Mode), !force && !b.meta.Existed)
	if replaced {
		b.meta.Existed, b.meta.DiskHash = true, digest(data)
	}
	if err != nil {
		return err
	}
	record := journalRecord{Kind: "saved", Version: b.version, Meta: &b.meta}
	committed, err := b.append(record)
	if committed {
		b.savedVersion = b.version
	}
	if err == nil && b.journal != nil {
		err = b.journal.Sync()
	}
	return err
}

func atomicWrite(path string, data []byte, mode os.FileMode, noReplace ...bool) (bool, error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".govim-write-*")
	if err != nil {
		return false, err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err = f.Chmod(mode.Perm()); err != nil {
		return false, err
	}
	if err = writeAll(f, data); err != nil {
		return false, err
	}
	if err = f.Sync(); err != nil {
		return false, err
	}
	if err = f.Close(); err != nil {
		return false, err
	}
	if len(noReplace) > 0 && noReplace[0] {
		err = unix.Renameat2(unix.AT_FDCWD, name, unix.AT_FDCWD, path, unix.RENAME_NOREPLACE)
	} else {
		err = os.Rename(name, path)
	}
	if err != nil {
		return false, err
	}
	return true, syncDirectory(dir)
}

func clamp(value, low, high int) int {
	if high < low {
		return low
	}
	return max(low, min(value, high))
}
