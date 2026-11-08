package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

type docTestStream struct {
	name string
	data []byte
}

// Generate small MS-CFB containers without relying on an Office installation.
// Both normal and mini-sector streams, v3/v4 headers and extended DIFATs are
// supported so the real stream reader is exercised, not mocked.
func makeDocTestOLE(streams []docTestStream, sectorSize, paddingSectors int, miniPadding ...int) []byte {
	var sectors [][]byte
	var fat []uint32
	allocate := func(data []byte) uint32 {
		if len(data) == 0 {
			return 0xfffffffe
		}
		first := uint32(len(sectors))
		for offset := 0; offset < len(data); offset += sectorSize {
			block := make([]byte, sectorSize)
			copy(block, data[offset:min(len(data), offset+sectorSize)])
			sectors = append(sectors, block)
			fat = append(fat, uint32(len(sectors)))
		}
		fat[len(fat)-1] = 0xfffffffe
		return first
	}
	starts := make([]uint32, len(streams))
	var miniData []byte
	var miniFAT []uint32
	for index, stream := range streams {
		if len(stream.data) == 0 || len(stream.data) >= 4096 {
			starts[index] = allocate(stream.data)
			continue
		}
		starts[index] = uint32(len(miniFAT))
		for offset := 0; offset < len(stream.data); offset += 64 {
			block := make([]byte, 64)
			copy(block, stream.data[offset:min(len(stream.data), offset+64)])
			miniData = append(miniData, block...)
			miniFAT = append(miniFAT, uint32(len(miniFAT)+1))
		}
		miniFAT[len(miniFAT)-1] = 0xfffffffe
	}
	if len(miniPadding) > 0 {
		padding := (miniPadding[0] + 63) / 64
		miniData = append(miniData, make([]byte, padding*64)...)
		for index := 0; index < padding; index++ {
			miniFAT = append(miniFAT, 0xffffffff)
		}
	}
	miniStart := allocate(miniData)
	miniFATBytes := make([]byte, ((len(miniFAT)*4+sectorSize-1)/sectorSize)*sectorSize)
	for offset := 0; offset < len(miniFATBytes); offset += 4 {
		binary.LittleEndian.PutUint32(miniFATBytes[offset:], 0xffffffff)
	}
	for index, value := range miniFAT {
		binary.LittleEndian.PutUint32(miniFATBytes[index*4:], value)
	}
	miniFATStart := allocate(miniFATBytes)
	directory := make([]byte, ((len(streams)+1)*128+sectorSize-1)/sectorSize*sectorSize)
	writeEntry := func(index int, name string, kind byte, start uint32, size uint64) {
		entry := directory[index*128:]
		units := utf16.Encode([]rune(name + "\x00"))
		for index, value := range units {
			binary.LittleEndian.PutUint16(entry[index*2:], value)
		}
		binary.LittleEndian.PutUint16(entry[64:], uint16(len(units)*2))
		entry[66], entry[67] = kind, 1
		for _, offset := range []int{68, 72, 76} {
			binary.LittleEndian.PutUint32(entry[offset:], 0xffffffff)
		}
		binary.LittleEndian.PutUint32(entry[116:], start)
		binary.LittleEndian.PutUint64(entry[120:], size)
	}
	writeEntry(0, "Root Entry", 5, miniStart, uint64(len(miniData)))
	if len(streams) > 0 {
		binary.LittleEndian.PutUint32(directory[76:], 1)
	}
	for index, stream := range streams {
		writeEntry(index+1, stream.name, 2, starts[index], uint64(len(stream.data)))
		if index+1 < len(streams) {
			binary.LittleEndian.PutUint32(directory[(index+1)*128+72:], uint32(index+2))
		}
	}
	directoryStart := allocate(directory)
	for index := 0; index < paddingSectors; index++ {
		sectors = append(sectors, make([]byte, sectorSize))
		fat = append(fat, 0xffffffff)
	}
	entriesPerFAT := sectorSize / 4
	fatCount, difatCount := 1, 0
	for {
		total := len(sectors) + fatCount + difatCount
		nextFAT := (total + entriesPerFAT - 1) / entriesPerFAT
		nextDIFAT := max(0, (nextFAT-109+entriesPerFAT-2)/(entriesPerFAT-1))
		if nextFAT == fatCount && nextDIFAT == difatCount {
			break
		}
		fatCount, difatCount = nextFAT, nextDIFAT
	}
	fatStart := len(sectors)
	for index := 0; index < fatCount; index++ {
		sectors = append(sectors, bytes.Repeat([]byte{0xff}, sectorSize))
		fat = append(fat, 0xfffffffd)
	}
	difatStart := len(sectors)
	for index := 0; index < difatCount; index++ {
		sectors = append(sectors, bytes.Repeat([]byte{0xff}, sectorSize))
		fat = append(fat, 0xfffffffc)
	}
	for index, value := range fat {
		binary.LittleEndian.PutUint32(sectors[fatStart+index/entriesPerFAT][(index%entriesPerFAT)*4:], value)
	}
	header := make([]byte, sectorSize)
	copy(header, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})
	binary.LittleEndian.PutUint16(header[24:], 0x003e)
	version, shift := uint16(3), uint16(9)
	if sectorSize == 4096 {
		version, shift = 4, 12
		binary.LittleEndian.PutUint32(header[40:], uint32(len(directory)/sectorSize))
	}
	binary.LittleEndian.PutUint16(header[26:], version)
	binary.LittleEndian.PutUint16(header[28:], 0xfffe)
	binary.LittleEndian.PutUint16(header[30:], shift)
	binary.LittleEndian.PutUint16(header[32:], 6)
	binary.LittleEndian.PutUint32(header[44:], uint32(fatCount))
	binary.LittleEndian.PutUint32(header[48:], directoryStart)
	binary.LittleEndian.PutUint32(header[56:], 4096)
	binary.LittleEndian.PutUint32(header[60:], miniFATStart)
	binary.LittleEndian.PutUint32(header[64:], uint32(len(miniFATBytes)/sectorSize))
	binary.LittleEndian.PutUint32(header[68:], 0xfffffffe)
	binary.LittleEndian.PutUint32(header[72:], uint32(difatCount))
	for index := 0; index < 109; index++ {
		id := uint32(0xffffffff)
		if index < fatCount {
			id = uint32(fatStart + index)
		}
		binary.LittleEndian.PutUint32(header[76+index*4:], id)
	}
	if difatCount > 0 {
		binary.LittleEndian.PutUint32(header[68:], uint32(difatStart))
	}
	for index := 0; index < difatCount; index++ {
		block := sectors[difatStart+index]
		for item := 0; item < entriesPerFAT-1; item++ {
			fatIndex := 109 + index*(entriesPerFAT-1) + item
			if fatIndex < fatCount {
				binary.LittleEndian.PutUint32(block[item*4:], uint32(fatStart+fatIndex))
			}
		}
		next := uint32(0xfffffffe)
		if index+1 < difatCount {
			next = uint32(difatStart + index + 1)
		}
		binary.LittleEndian.PutUint32(block[sectorSize-4:], next)
	}
	var output bytes.Buffer
	output.Write(header)
	for _, sector := range sectors {
		output.Write(sector)
	}
	return output.Bytes()
}

type docTestPiece struct {
	units      []uint16
	compressed bool
}

func makeDocTestStreams(pieces []docTestPiece, oneTable bool) []docTestStream {
	word := make([]byte, 2048)
	positions := make([]uint32, len(pieces))
	// Deliberately store text in reverse physical order. Only the piece table
	// recovers the original order, unlike fcMin/fcMac or string scanning.
	for index := len(pieces) - 1; index >= 0; index-- {
		piece := pieces[index]
		positions[index] = uint32(len(word))
		if piece.compressed {
			positions[index] = positions[index]*2 | 0x40000000
		}
		for _, unit := range piece.units {
			word = append(word, byte(unit))
			if !piece.compressed {
				word = append(word, byte(unit>>8))
			}
		}
	}
	plc := make([]byte, 4+12*len(pieces))
	cp := uint32(0)
	for index, piece := range pieces {
		binary.LittleEndian.PutUint32(plc[index*4:], cp)
		binary.LittleEndian.PutUint32(plc[(len(pieces)+1)*4+index*8+2:], positions[index])
		cp += uint32(len(piece.units))
	}
	binary.LittleEndian.PutUint32(plc[len(pieces)*4:], cp)
	clx := []byte{1, 3, 0, 0xaa, 0xbb, 0xcc, 2, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(clx[7:], uint32(len(plc)))
	clx = append(clx, plc...)
	table := append(make([]byte, 32), clx...)
	binary.LittleEndian.PutUint16(word, 0xa5ec)
	binary.LittleEndian.PutUint16(word[2:], 0x00c1)
	flags := uint16(0x1000)
	name := "0Table"
	if oneTable {
		flags |= 0x0200
		name = "1Table"
	}
	binary.LittleEndian.PutUint16(word[10:], flags)
	binary.LittleEndian.PutUint16(word[32:], 14)
	binary.LittleEndian.PutUint16(word[62:], 22)
	binary.LittleEndian.PutUint32(word[0x4c:], cp)
	binary.LittleEndian.PutUint16(word[0x98:], 0x005d)
	binary.LittleEndian.PutUint32(word[0x1a2:], 32)
	binary.LittleEndian.PutUint32(word[0x1a6:], uint32(len(clx)))
	return []docTestStream{{"WordDocument", word}, {name, table}}
}

func docUnicodePiece(text string) docTestPiece {
	return docTestPiece{units: utf16.Encode([]rune(text))}
}

func readDocTestText(data []byte) (string, error) {
	ole, err := openDocOLE(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	return extractDOCText(ole)
}

func TestDOCMixedPiecesAndOLEVariants(t *testing.T) {
	pieces := []docTestPiece{
		{units: []uint16{'H', 'e', 'l', 'l', 'o', ' ', 0x93, 'c', 'a', 'f', 0xe9, 0x94, '\r'}, compressed: true},
		docUnicodePiece("世界 "),
		{units: []uint16{0xd83d}}, {units: []uint16{0xde00, '\r'}},
	}
	for _, tc := range []struct {
		name                           string
		sector, wordPad, tablePad, pad int
		oneTable                       bool
	}{
		{name: "v3 mini streams", sector: 512},
		{name: "v3 regular Word and mini Table", sector: 512, wordPad: 4096, oneTable: true},
		{name: "v3 regular streams", sector: 512, wordPad: 4096, tablePad: 4096},
		{name: "v4 mini streams", sector: 4096, oneTable: true},
		{name: "v4 regular streams", sector: 4096, wordPad: 4096, tablePad: 4096},
		{name: "extended DIFAT", sector: 512, pad: 14000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streams := makeDocTestStreams(pieces, tc.oneTable)
			if tc.wordPad > len(streams[0].data) {
				streams[0].data = append(streams[0].data, make([]byte, tc.wordPad-len(streams[0].data))...)
			}
			if tc.tablePad > len(streams[1].data) {
				streams[1].data = append(streams[1].data, make([]byte, tc.tablePad-len(streams[1].data))...)
			}
			data := makeDocTestOLE(streams, tc.sector, tc.pad)
			text, err := readDocTestText(data)
			if err != nil || text != "Hello “café”\n世界 😀\n" {
				t.Fatalf("DOC text = %q, %v", text, err)
			}
		})
	}
}

func TestDOCMainStoryAndSavedFieldResults(t *testing.T) {
	main := "Body\rCell A\x07Cell B\r\x13HYPERLINK secret-target\x14Click here\x15\r" +
		"\x13IF \x13hidden nested\x14nested-code-result\x15\x14Visible \x13DATE\x14today\x15\x15\r\x01\x02\x1bSafe\r"
	streams := makeDocTestStreams([]docTestPiece{docUnicodePiece(main), docUnicodePiece("footnote-marker\rheader-marker\r")}, true)
	binary.LittleEndian.PutUint32(streams[0].data[0x4c:], uint32(len(utf16.Encode([]rune(main)))))
	text, err := readDocTestText(makeDocTestOLE(streams, 512, 0))
	if err != nil || text != "Body\nCell A\tCell B\nClick here\nVisible today\nSafe\n" {
		t.Fatalf("DOC main text = %q, %v", text, err)
	}
}

func TestDOCWalksCountedFIBSections(t *testing.T) {
	streams := makeDocTestStreams([]docTestPiece{docUnicodePiece("Counted sections\r")}, false)
	word := streams[0].data
	copy(word[64:902], word[62:900]) // One additional WORD in FibRgW.
	binary.LittleEndian.PutUint16(word[32:], 15)
	// Word 2003 can leave nFib at Word 97 and specify nFibNew instead.
	binary.LittleEndian.PutUint16(word[900:], 1)
	binary.LittleEndian.PutUint16(word[902:], 0x010c)
	text, err := readDocTestText(makeDocTestOLE(streams, 512, 0))
	if err != nil || text != "Counted sections\n" {
		t.Fatalf("variable FIB text = %q, %v", text, err)
	}
}

type docReadCounter struct {
	*bytes.Reader
	read int
}

func (reader *docReadCounter) ReadAt(data []byte, offset int64) (int, error) {
	n, err := reader.Reader.ReadAt(data, offset)
	reader.read += n
	return n, err
}

func TestDOCTextRangeReadsSkipLargePayloads(t *testing.T) {
	const expected = "Text from a picture-heavy DOC.\n"
	for _, name := range []string{"WordDocument", "Table", "root mini stream"} {
		t.Run(name, func(t *testing.T) {
			streams := makeDocTestStreams([]docTestPiece{docUnicodePiece(strings.ReplaceAll(expected, "\n", "\r"))}, false)
			padding := maxPreviewDocumentSize + 512
			miniPadding := 0
			switch name {
			case "WordDocument":
				streams[0].data = append(streams[0].data, make([]byte, padding)...)
			case "Table":
				// Put CLX after the irrelevant large payload so reads must seek,
				// not just consume a small prefix of every stream.
				clx := append([]byte(nil), streams[1].data[32:]...)
				streams[1].data = append(make([]byte, padding), clx...)
				binary.LittleEndian.PutUint32(streams[0].data[0x1a2:], uint32(padding))
			case "root mini stream":
				miniPadding = padding
			}
			data := makeDocTestOLE(streams, 512, 0, miniPadding)
			reader := &docReadCounter{Reader: bytes.NewReader(data)}
			ole, err := openDocOLE(reader, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			text, err := extractDOCText(ole)
			if err != nil || text != expected {
				t.Fatalf("large-stream DOC text = %q, %v", text, err)
			}
			if reader.read > 2<<20 {
				t.Fatalf("read %d bytes from a %d-byte DOC; irrelevant payload was not skipped", reader.read, len(data))
			}
		})
	}
}

func TestDOCToleratesUnallocatedTrailer(t *testing.T) {
	streams := makeDocTestStreams([]docTestPiece{docUnicodePiece("Trailer-safe text\r")}, false)
	data := makeDocTestOLE(streams, 512, 0)
	// Include enough extra bytes to extend beyond the FAT's coverage, plus an
	// unaligned tail. None belongs to a stream referenced by the directory.
	data = append(data, make([]byte, 128*512+17)...)
	text, err := readDocTestText(data)
	if err != nil || text != "Trailer-safe text\n" {
		t.Fatalf("DOC with trailer = %q, %v", text, err)
	}
}

func TestDOCDistinguishesContainerLimitFromTruncation(t *testing.T) {
	streams := makeDocTestStreams([]docTestPiece{docUnicodePiece("Text\r")}, false)
	data := makeDocTestOLE(streams, 512, 0)
	if _, err := openDocOLE(bytes.NewReader(data), maxOfficeArchiveSize+1); err == nil ||
		!strings.Contains(err.Error(), "limit") || strings.Contains(err.Error(), "truncated") {
		t.Fatalf("oversized-container error = %v", err)
	}
	if _, err := openDocOLE(bytes.NewReader(data), 512); err == nil ||
		!strings.Contains(err.Error(), "truncated") || strings.Contains(err.Error(), "limit") {
		t.Fatalf("short-container error = %v", err)
	}
}

func TestDOCPartialFinalDataSectorNeedsOnlyReferencedBytes(t *testing.T) {
	const source = "Unpadded text\r"
	streams := makeDocTestStreams([]docTestPiece{docUnicodePiece(source)}, false)
	streams[0].data = append(streams[0].data, make([]byte, 4096-len(streams[0].data))...)
	data := makeDocTestOLE(streams, 512, 0)
	fat := (int(binary.LittleEndian.Uint32(data[76:])) + 1) * 512
	newSector := uint32(len(data)/512 - 1)
	// Move WordDocument's text sector (logical offset 2048) to a partial final
	// physical sector. All text exists; only unused padding is absent.
	next := binary.LittleEndian.Uint32(data[fat+4*4:])
	binary.LittleEndian.PutUint32(data[fat+3*4:], newSector)
	binary.LittleEndian.PutUint32(data[fat+int(newSector)*4:], next)
	binary.LittleEndian.PutUint32(data[fat+4*4:], 0xffffffff)
	textBytes := 2 * len(source)
	data = append(data, data[5*512:5*512+textBytes]...)
	text, err := readDocTestText(data)
	if err != nil || text != "Unpadded text\n" {
		t.Fatalf("DOC missing unused padding = %q, %v", text, err)
	}
	if _, err := readDocTestText(data[:len(data)-1]); err == nil {
		t.Fatal("missing referenced text byte was silently accepted")
	}
}

func TestDOCRejectsUnsupportedAndMalformedWordStreams(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]docTestStream)
	}{
		{"old Word", func(s []docTestStream) { binary.LittleEndian.PutUint16(s[0].data[2:], 0x0065) }},
		{"unknown version", func(s []docTestStream) { binary.LittleEndian.PutUint16(s[0].data[2:], 0xffff) }},
		{"encrypted", func(s []docTestStream) { binary.LittleEndian.PutUint16(s[0].data[10:], 0x1100) }},
		{"obfuscated", func(s []docTestStream) { binary.LittleEndian.PutUint16(s[0].data[10:], 0x9100) }},
		{"bad FIB count", func(s []docTestStream) { binary.LittleEndian.PutUint16(s[0].data[32:], 0xffff) }},
		{"no CLX", func(s []docTestStream) { binary.LittleEndian.PutUint32(s[0].data[0x1a6:], 0) }},
		{"bad CLX range", func(s []docTestStream) { binary.LittleEndian.PutUint32(s[0].data[0x1a2:], 0xffffffff) }},
		{"oversized text", func(s []docTestStream) { binary.LittleEndian.PutUint32(s[0].data[0x4c:], maxOfficeTextSize+1) }},
		{"wrong Table selection", func(s []docTestStream) { s[1].name = "1Table" }},
		{"bad CP origin", func(s []docTestStream) { binary.LittleEndian.PutUint32(s[1].data[43:], 1) }},
		{"bad text offset", func(s []docTestStream) { binary.LittleEndian.PutUint32(s[1].data[53:], 0x3fffffff) }},
		{"truncated formatting", func(s []docTestStream) { binary.LittleEndian.PutUint16(s[1].data[33:], 0xffff) }},
		{"bad piece size", func(s []docTestStream) { binary.LittleEndian.PutUint32(s[1].data[39:], 15) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streams := makeDocTestStreams([]docTestPiece{docUnicodePiece("Text\r")}, false)
			tc.mutate(streams)
			if text, err := readDocTestText(makeDocTestOLE(streams, 512, 0)); err == nil {
				t.Fatalf("malformed DOC returned %q", text)
			}
		})
	}
	if _, err := readDocTestText([]byte("{\\rtf1 Not a binary Word document}")); err == nil {
		t.Fatal("RTF masquerading as DOC was accepted")
	}
	if _, err := normalizeDOCText(utf16.Encode([]rune(strings.Repeat("\x13", 65)))); err == nil {
		t.Fatal("unbounded field nesting was accepted")
	}
	if _, err := normalizeDOCText(utf16.Encode([]rune(strings.Repeat("界", maxOfficeTextSize/3+1)))); err == nil {
		t.Fatal("oversized UTF-8 text was accepted")
	}
}

func TestDOCRejectsMalformedOLEChains(t *testing.T) {
	fixture := func() []byte {
		streams := makeDocTestStreams([]docTestPiece{docUnicodePiece("Safe text\r")}, false)
		return makeDocTestOLE(streams, 512, 0)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"bad sector shift", func(d []byte) { binary.LittleEndian.PutUint16(d[30:], 31) }},
		{"bad byte order", func(d []byte) { binary.LittleEndian.PutUint16(d[28:], 0xfeff) }},
		{"oversized FAT count", func(d []byte) { binary.LittleEndian.PutUint32(d[44:], 0xffffffff) }},
		{"out-of-file FAT", func(d []byte) { binary.LittleEndian.PutUint32(d[76:], 0xfffffff0) }},
		{"directory cycle", func(d []byte) {
			fat := (int(binary.LittleEndian.Uint32(d[76:])) + 1) * 512
			dir := binary.LittleEndian.Uint32(d[48:])
			binary.LittleEndian.PutUint32(d[fat+int(dir)*4:], dir)
		}},
		{"directory tree cycle", func(d []byte) {
			dir := (int(binary.LittleEndian.Uint32(d[48:])) + 1) * 512
			binary.LittleEndian.PutUint32(d[dir+128+72:], 1)
		}},
		{"mini sector cycle", func(d []byte) {
			miniFAT := (int(binary.LittleEndian.Uint32(d[60:])) + 1) * 512
			binary.LittleEndian.PutUint32(d[miniFAT:], 0)
		}},
		{"mini sector out of range", func(d []byte) {
			dir := (int(binary.LittleEndian.Uint32(d[48:])) + 1) * 512
			binary.LittleEndian.PutUint32(d[dir+128+116:], 0xffff)
		}},
		{"oversized selected stream", func(d []byte) {
			dir := (int(binary.LittleEndian.Uint32(d[48:])) + 1) * 512
			binary.LittleEndian.PutUint64(d[dir+128+120:], maxPreviewDocumentSize+1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := fixture()
			tc.mutate(data)
			if text, err := readDocTestText(data); err == nil {
				t.Fatalf("malformed OLE returned %q", text)
			}
		})
	}
	data := fixture()
	if _, err := readDocTestText(data[:len(data)-1]); err == nil {
		t.Fatal("truncated OLE was accepted")
	}
	// Nested WordDocument streams belong to embedded objects, not the file's
	// main document. Do not find them by scanning every directory record.
	dir := (int(binary.LittleEndian.Uint32(data[48:])) + 1) * 512
	data[dir+128+66] = 1 // Root child is now a storage.
	binary.LittleEndian.PutUint32(data[dir+128+72:], 0xffffffff)
	binary.LittleEndian.PutUint32(data[dir+128+76:], 2)
	if _, err := readDocTestText(data); err == nil {
		t.Fatal("embedded document was mistaken for the root document")
	}
}

func TestDOCPreviewPDFCacheAndPaging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "letter.DOC")
	write := func(text string) {
		t.Helper()
		streams := makeDocTestStreams([]docTestPiece{docUnicodePiece(text)}, true)
		if err := os.WriteFile(path, makeDocTestOLE(streams, 512, 0), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(strings.Repeat("A paragraph from a legacy Word document with enough text for paginated preview navigation.\r", 100))
	renderer := newPreviewDocumentRenderer(dir)
	preview, err := renderer.renderSized(path, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Kind != "DOC" || preview.pages() < 2 || !isRenderedDocument(path) || (&Browser{}).fileType(path) != "text" {
		t.Fatalf("unexpected DOC preview: %#v", preview)
	}
	pdf, err := os.ReadFile(preview.PDFPath)
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) || bytes.Contains(pdf, []byte("/Subtype /Image")) {
		t.Fatalf("DOC preview is not a native text PDF: %v", err)
	}
	if cached, err := renderer.renderSized(path, 120, 160); err != nil || cached != preview {
		t.Fatalf("DOC preview was not cached: %v", err)
	}
	browser := startBrowser(t, testEnv(), dir)
	// Cached PDFs sort before letter.DOC; select the source as the last entry.
	browser.send("\x1b[F")
	waitContains(t, browser, "DOC  1/", "DOC first page title")
	browser.send("]")
	waitContains(t, browser, "DOC  2/", "DOC second page title")
	waitContains(t, browser, " page 2", "DOC second page placement")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	write("Changed DOC text\r")
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	updated, err := renderer.renderSized(path, 120, 160)
	if err != nil || updated.PDFPath == preview.PDFPath || len(renderer.docs) != 1 {
		t.Fatalf("changed DOC reused its stale preview: %v", err)
	}
	if _, err := os.Stat(preview.PDFPath); !os.IsNotExist(err) {
		t.Fatalf("superseded DOC preview was not removed: %v", err)
	}
}

func FuzzDOCText(f *testing.F) {
	streams := makeDocTestStreams([]docTestPiece{docUnicodePiece("Fuzz DOC text\r")}, false)
	f.Add(makeDocTestOLE(streams, 512, 0))
	f.Add([]byte("not an OLE file"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		_, _ = readDocTestText(data)
	})
}

func FuzzDOCPieceTable(f *testing.F) {
	streams := makeDocTestStreams([]docTestPiece{docUnicodePiece("Unicode 世界\r")}, false)
	f.Add(streams[0].data, streams[1].data[32:], uint32(11))
	f.Add([]byte{}, []byte{}, uint32(0))
	f.Fuzz(func(t *testing.T, word, clx []byte, characters uint32) {
		if len(word) > maxPreviewDocumentSize || len(clx) > maxPreviewDocumentSize {
			return
		}
		_, _ = extractDOCPieces(bytes.NewReader(word), uint64(len(word)), clx, characters)
	})
}
