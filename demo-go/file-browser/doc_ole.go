package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

const (
	docOLEFree       = uint32(0xffffffff)
	docOLEEnd        = uint32(0xfffffffe)
	docOLEFAT        = uint32(0xfffffffd)
	docOLEDIFAT      = uint32(0xfffffffc)
	docOLEMiniCutoff = 4096
	maxDocDirectory  = 1 << 20
)

// A read-only subset of MS-CFB, scoped to legacy Word previews. Only streams
// directly beneath the root storage are exposed; embedded documents/macros
// are never traversed. Sector chains, metadata and stream reads are bounded.
// https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-cfb/
type docOLE struct {
	reader                  io.ReaderAt
	sectorSize, sectors     int
	fat, miniFAT            []uint32
	miniStream              *docOLEStream
	miniStart, miniFATStart uint32
	miniSize                uint64
	miniFATCount            uint32
	streams                 map[string]docOLEEntry
}

type docOLEEntry struct {
	name               string
	kind               byte
	left, right, child uint32
	start              uint32
	size               uint64
}

func openDocOLE(reader io.ReaderAt, size int64) (*docOLE, error) {
	header := make([]byte, 512)
	if _, err := reader.ReadAt(header, 0); err != nil {
		return nil, fmt.Errorf("read DOC OLE header: %w", err)
	}
	if !bytes.Equal(header[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
		return nil, fmt.Errorf("unsupported DOC format: expected a Word 97–2003 OLE file")
	}
	version := binary.LittleEndian.Uint16(header[26:])
	shift := binary.LittleEndian.Uint16(header[30:])
	if binary.LittleEndian.Uint16(header[28:]) != 0xfffe ||
		!((version == 3 && shift == 9) || (version == 4 && shift == 12)) ||
		binary.LittleEndian.Uint16(header[32:]) != 6 ||
		binary.LittleEndian.Uint32(header[56:]) != docOLEMiniCutoff {
		return nil, fmt.Errorf("unsupported DOC OLE sector format")
	}
	sectorSize := 1 << shift
	if size > maxOfficeArchiveSize {
		return nil, fmt.Errorf("DOC file exceeds the %d-byte preview limit", maxOfficeArchiveSize)
	}
	if size < int64(2*sectorSize) {
		return nil, fmt.Errorf("DOC OLE file is truncated: not enough data for its sectors")
	}
	ole := &docOLE{
		// Files may have an unallocated trailer or omit unused padding in their
		// last data sector. Actual metadata/text reads still require real bytes.
		reader: reader, sectorSize: sectorSize, sectors: int((size+int64(sectorSize)-1)/int64(sectorSize)) - 1,
		miniFATStart: binary.LittleEndian.Uint32(header[60:]),
		miniFATCount: binary.LittleEndian.Uint32(header[64:]),
		streams:      make(map[string]docOLEEntry),
	}
	if err := ole.loadFAT(header); err != nil {
		return nil, err
	}
	directory, err := ole.readChain(binary.LittleEndian.Uint32(header[48:]), -1, maxDocDirectory)
	if err != nil {
		return nil, fmt.Errorf("read DOC OLE directory: %w", err)
	}
	directoryCount := binary.LittleEndian.Uint32(header[40:])
	if (version == 3 && directoryCount != 0) || (version == 4 && uint64(directoryCount)*uint64(sectorSize) != uint64(len(directory))) {
		return nil, fmt.Errorf("invalid DOC OLE directory size")
	}
	if err := ole.loadDirectory(directory, version); err != nil {
		return nil, err
	}
	return ole, nil
}

func (ole *docOLE) sector(id uint32) ([]byte, error) {
	if uint64(id) >= uint64(ole.sectors) {
		return nil, fmt.Errorf("invalid DOC OLE sector %d", id)
	}
	data := make([]byte, ole.sectorSize)
	_, err := ole.reader.ReadAt(data, int64(uint64(id)+1)*int64(ole.sectorSize))
	return data, err
}

func (ole *docOLE) loadFAT(header []byte) error {
	fatCount := binary.LittleEndian.Uint32(header[44:])
	difatCount := binary.LittleEndian.Uint32(header[72:])
	if fatCount == 0 || uint64(fatCount)+uint64(difatCount) > uint64(ole.sectors) ||
		(uint64(fatCount)+uint64(difatCount))*uint64(ole.sectorSize) > maxPreviewDocumentSize {
		return fmt.Errorf("invalid or oversized DOC OLE allocation tables")
	}
	var fatSectors []uint32
	roles := make(map[uint32]uint32)
	addFAT := func(id uint32) error {
		if id == docOLEFree {
			return nil
		}
		if uint64(id) >= uint64(ole.sectors) || roles[id] != 0 || len(fatSectors) >= int(fatCount) {
			return fmt.Errorf("invalid or duplicate DOC OLE FAT sector")
		}
		roles[id] = docOLEFAT
		fatSectors = append(fatSectors, id)
		return nil
	}
	for offset := 76; offset < 512; offset += 4 {
		if err := addFAT(binary.LittleEndian.Uint32(header[offset:])); err != nil {
			return err
		}
	}
	next := binary.LittleEndian.Uint32(header[68:])
	for index := uint32(0); index < difatCount; index++ {
		if uint64(next) >= uint64(ole.sectors) || roles[next] != 0 {
			return fmt.Errorf("invalid or cyclic DOC OLE DIFAT chain")
		}
		roles[next] = docOLEDIFAT
		data, err := ole.sector(next)
		if err != nil {
			return err
		}
		for offset := 0; offset < len(data)-4; offset += 4 {
			if err := addFAT(binary.LittleEndian.Uint32(data[offset:])); err != nil {
				return err
			}
		}
		next = binary.LittleEndian.Uint32(data[len(data)-4:])
	}
	if (next != docOLEEnd && !(difatCount == 0 && next == docOLEFree)) || len(fatSectors) != int(fatCount) {
		return fmt.Errorf("truncated DOC OLE DIFAT")
	}
	for _, id := range fatSectors {
		data, err := ole.sector(id)
		if err != nil {
			return err
		}
		for offset := 0; offset < len(data) && len(ole.fat) < ole.sectors; offset += 4 {
			ole.fat = append(ole.fat, binary.LittleEndian.Uint32(data[offset:]))
		}
	}
	// Unallocated trailing data need not be covered by the FAT. Every sector
	// referenced by a directory/stream chain is checked independently below.
	for id, role := range roles {
		if uint64(id) >= uint64(len(ole.fat)) || ole.fat[id] != role {
			return fmt.Errorf("invalid DOC OLE allocation-table marker")
		}
	}
	return nil
}

// size == -1 reads an unknown-length directory chain through ENDOFCHAIN.
func (ole *docOLE) readChain(start uint32, size int64, limit int) ([]byte, error) {
	if size > int64(limit) {
		return nil, fmt.Errorf("DOC OLE stream exceeds %d bytes", limit)
	}
	if size == 0 {
		return nil, nil
	}
	var data []byte
	seen := make(map[uint32]bool)
	for id := start; id != docOLEEnd; {
		if uint64(id) >= uint64(len(ole.fat)) || seen[id] {
			return nil, fmt.Errorf("invalid or cyclic DOC OLE stream chain")
		}
		seen[id] = true
		if size >= 0 && int64(len(data)) >= size {
			return nil, fmt.Errorf("DOC OLE stream chain is longer than its declared size")
		}
		block, err := ole.sector(id)
		if err != nil {
			return nil, err
		}
		count := ole.sectorSize
		if size >= 0 {
			count = min(count, int(size)-len(data))
		}
		if count > limit-len(data) {
			return nil, fmt.Errorf("DOC OLE stream exceeds %d bytes", limit)
		}
		data = append(data, block[:count]...)
		id = ole.fat[id]
	}
	if size >= 0 && int64(len(data)) != size {
		return nil, fmt.Errorf("DOC OLE stream chain is truncated")
	}
	return data, nil
}

func (ole *docOLE) loadDirectory(data []byte, version uint16) error {
	if len(data) < 128 {
		return fmt.Errorf("DOC OLE has no root directory")
	}
	entries := make([]docOLEEntry, len(data)/128)
	for index := range entries {
		entry := data[index*128 : (index+1)*128]
		if entry[66] == 0 {
			continue // Unallocated entries have no meaningful fields.
		}
		nameBytes := int(binary.LittleEndian.Uint16(entry[64:]))
		if nameBytes < 2 || nameBytes > 64 || nameBytes%2 != 0 || binary.LittleEndian.Uint16(entry[nameBytes-2:]) != 0 {
			return fmt.Errorf("invalid DOC OLE directory name")
		}
		units := make([]uint16, nameBytes/2-1)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(entry[i*2:])
		}
		size := binary.LittleEndian.Uint64(entry[120:])
		if version == 3 {
			// MS-CFB readers ignore the high DWORD in version 3 directories.
			size = uint64(binary.LittleEndian.Uint32(entry[120:]))
		}
		entries[index] = docOLEEntry{
			name: string(utf16.Decode(units)), kind: entry[66],
			left: binary.LittleEndian.Uint32(entry[68:]), right: binary.LittleEndian.Uint32(entry[72:]),
			child: binary.LittleEndian.Uint32(entry[76:]), start: binary.LittleEndian.Uint32(entry[116:]), size: size,
		}
	}
	root := entries[0]
	if root.kind != 5 {
		return fmt.Errorf("DOC OLE directory has no root storage")
	}
	ole.miniStart, ole.miniSize = root.start, root.size
	pending, seen := []uint32{root.child}, make(map[uint32]bool)
	for len(pending) > 0 {
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if id == docOLEFree {
			continue
		}
		if id == 0 || uint64(id) >= uint64(len(entries)) || seen[id] {
			return fmt.Errorf("invalid or cyclic DOC OLE directory tree")
		}
		seen[id] = true
		entry := entries[id]
		if entry.kind != 1 && entry.kind != 2 {
			return fmt.Errorf("invalid DOC OLE directory object")
		}
		pending = append(pending, entry.left, entry.right)
		if entry.kind == 2 {
			name := strings.ToLower(entry.name)
			if _, duplicate := ole.streams[name]; duplicate {
				return fmt.Errorf("duplicate DOC OLE stream %q", entry.name)
			}
			ole.streams[name] = entry
		}
	}
	return nil
}

func (ole *docOLE) stream(name string) (*docOLEStream, error) {
	entry, ok := ole.streams[strings.ToLower(name)]
	if !ok {
		return nil, fmt.Errorf("DOC OLE is missing the %s stream", name)
	}
	mini := entry.size > 0 && entry.size < docOLEMiniCutoff
	if mini && ole.miniFAT == nil {
		if ole.miniFATCount == 0 {
			return nil, fmt.Errorf("DOC OLE has no mini allocation table")
		}
		fatData, err := ole.readChain(ole.miniFATStart, int64(ole.miniFATCount)*int64(ole.sectorSize), maxPreviewDocumentSize)
		if err != nil {
			return nil, err
		}
		ole.miniStream, err = ole.chainStream(ole.miniStart, ole.miniSize, false)
		if err != nil {
			return nil, err
		}
		for offset := 0; offset < len(fatData); offset += 4 {
			ole.miniFAT = append(ole.miniFAT, binary.LittleEndian.Uint32(fatData[offset:]))
		}
	}
	return ole.chainStream(entry.start, entry.size, mini)
}

// Index allocation chains, not their payloads. Large pictures/formatting can
// inflate WordDocument, Table or the root mini stream without inflating a text
// preview. Only FIB/CLX/text ranges are subsequently read through ReaderAt.
type docOLEStream struct {
	ole        *docOLE
	size       uint64
	sectors    []uint32
	sectorSize int
	mini       bool
}

func (ole *docOLE) chainStream(start uint32, size uint64, mini bool) (*docOLEStream, error) {
	table, sectorSize := ole.fat, ole.sectorSize
	if mini {
		table, sectorSize = ole.miniFAT, 64
	}
	count := size / uint64(sectorSize)
	if size%uint64(sectorSize) != 0 {
		count++
	}
	if count > uint64(len(table)) {
		return nil, fmt.Errorf("DOC OLE stream size exceeds its allocation table")
	}
	stream := &docOLEStream{ole: ole, size: size, sectorSize: sectorSize, mini: mini}
	if size == 0 {
		return stream, nil
	}
	seen := make([]bool, len(table))
	id := start
	for index := uint64(0); index < count; index++ {
		if uint64(id) >= uint64(len(table)) || seen[id] || (!mini && uint64(id) >= uint64(ole.sectors)) {
			return nil, fmt.Errorf("invalid, truncated or cyclic DOC OLE stream chain")
		}
		seen[id] = true
		stream.sectors = append(stream.sectors, id)
		id = table[id]
	}
	if id != docOLEEnd {
		return nil, fmt.Errorf("DOC OLE stream chain is longer than its declared size")
	}
	return stream, nil
}

func (stream *docOLEStream) ReadAt(data []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("invalid negative DOC stream offset")
	}
	if len(data) == 0 {
		return 0, nil
	}
	if uint64(offset) >= stream.size {
		return 0, io.EOF
	}
	count := int(min(uint64(len(data)), stream.size-uint64(offset)))
	read := 0
	for read < count {
		position := uint64(offset) + uint64(read)
		id := stream.sectors[position/uint64(stream.sectorSize)]
		within := int(position % uint64(stream.sectorSize))
		length := min(count-read, stream.sectorSize-within)
		var reader io.ReaderAt = stream.ole.reader
		physical := (uint64(id)+1)*uint64(stream.sectorSize) + uint64(within)
		if stream.mini {
			reader = stream.ole.miniStream
			physical = uint64(id)*64 + uint64(within)
		}
		n, err := reader.ReadAt(data[read:read+length], int64(physical))
		read += n
		if err != nil {
			return read, err
		}
		if n != length {
			return read, io.ErrUnexpectedEOF
		}
	}
	if read < len(data) {
		return read, io.EOF
	}
	return read, nil
}

func (stream *docOLEStream) readRange(offset, length uint64) ([]byte, error) {
	return readDOCRange(stream, stream.size, offset, length)
}

func readDOCRange(reader io.ReaderAt, size, offset, length uint64) ([]byte, error) {
	if offset > size || length > size-offset {
		return nil, fmt.Errorf("DOC text/metadata range is outside its stream")
	}
	if length > maxPreviewDocumentSize {
		return nil, fmt.Errorf("DOC text/metadata range exceeds the %d-byte limit", maxPreviewDocumentSize)
	}
	data := make([]byte, int(length))
	if length == 0 {
		return data, nil
	}
	n, err := reader.ReadAt(data, int64(offset))
	if err != nil {
		return nil, fmt.Errorf("DOC text/metadata is truncated: %w", err)
	}
	if n != len(data) {
		return nil, fmt.Errorf("DOC text/metadata is truncated: %w", io.ErrUnexpectedEOF)
	}
	return data, nil
}
