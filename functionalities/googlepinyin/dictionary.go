// Package googlepinyin implements the bounded dictionary/search frontend used
// by Google Pinyin without linking its native decoder.
package googlepinyin

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	dictionaryHeader          = "st-go-googlepinyin-v1"
	MaxCompressedDictionary   = 2 << 20
	MaxUncompressedDictionary = 8 << 20
	MaxRawDictionary          = 8 << 20
	MaxEntries                = 100000
	MaxPrompt                 = 32
	MaxCandidates             = 4096
	maxRecordBytes            = 512
	maxCandidateRunes         = 32
	maxSyllables              = 8
	maxSyllableBytes          = 8
	minimumPhraseFrequency    = 60
)

type entry struct {
	text      string
	frequency float64
	syllables []string
}

// Decoder is immutable after loading and safe for concurrent lookups.
type Decoder struct {
	entries   [][]entry // indexed by syllable count
	syllables map[string]struct{}
	prefixes  map[string]struct{}
}

// BuildDictionary converts the pinned upstream UTF-16LE raw corpus to the
// bounded, architecture-independent gzip format consumed by Open. The valid
// character list is applied before any record is emitted.
func BuildDictionary(raw, valid io.Reader, output io.Writer) error {
	validText, err := readUTF16LE(valid, MaxRawDictionary)
	if err != nil {
		return fmt.Errorf("valid-character list: %w", err)
	}
	allowed := make(map[rune]struct{}, len(validText))
	for _, character := range validText {
		if !unicode.IsSpace(character) {
			allowed[character] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return fmt.Errorf("valid-character list is empty")
	}
	rawText, err := readUTF16LE(raw, MaxRawDictionary)
	if err != nil {
		return fmt.Errorf("raw dictionary: %w", err)
	}
	type record struct {
		text, frequency, pinyin string
	}
	records := make([]record, 0, 65536)
	scanner := bufio.NewScanner(strings.NewReader(rawText))
	scanner.Buffer(make([]byte, 256), maxRecordBytes)
	recordNumber := 0
	for scanner.Scan() {
		recordNumber++
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || len(fields)-3 > maxSyllables {
			return fmt.Errorf("invalid raw dictionary record %d", recordNumber)
		}
		text := fields[0]
		if !validCandidate(text, len(fields)-3) {
			return fmt.Errorf("invalid raw candidate at record %d", recordNumber)
		}
		permitted := true
		for _, character := range text {
			if _, ok := allowed[character]; !ok {
				permitted = false
				break
			}
		}
		if !permitted {
			continue
		}
		frequency, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || frequency <= 0 || math.IsInf(frequency, 0) || math.IsNaN(frequency) {
			return fmt.Errorf("invalid frequency at record %d", recordNumber)
		}
		if fields[2] != "0" && fields[2] != "1" {
			return fmt.Errorf("invalid raw flags at record %d", recordNumber)
		}
		for _, syllable := range fields[3:] {
			if !validSyllable(syllable) {
				return fmt.Errorf("invalid spelling at record %d", recordNumber)
			}
		}
		// Keep the same noise cutoff as the upstream dictionary builder. Its
		// single-character entries are never filtered by this threshold.
		if len(fields)-3 > 1 && frequency < minimumPhraseFrequency {
			continue
		}
		records = append(records, record{text, strconv.FormatFloat(frequency, 'g', -1, 64), strings.Join(fields[3:], "'")})
		if len(records) > MaxEntries {
			return fmt.Errorf("dictionary exceeds %d entries", MaxEntries)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(records) == 0 {
		return fmt.Errorf("dictionary has no entries")
	}
	zipper, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		return err
	}
	zipper.Header.OS = 255
	writer := bufio.NewWriterSize(zipper, 64<<10)
	if _, err = fmt.Fprintf(writer, "%s\t%d\n", dictionaryHeader, len(records)); err == nil {
		for _, record := range records {
			if _, err = fmt.Fprintf(writer, "%s\t%s\t%s\n", record.text, record.frequency, record.pinyin); err != nil {
				break
			}
		}
	}
	if flushErr := writer.Flush(); err == nil {
		err = flushErr
	}
	if closeErr := zipper.Close(); err == nil {
		err = closeErr
	}
	return err
}

func readUTF16LE(reader io.Reader, maximum int64) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maximum {
		return "", fmt.Errorf("input exceeds %d bytes", maximum)
	}
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xfe || len(data)%2 != 0 {
		return "", fmt.Errorf("expected even-length UTF-16LE with BOM")
	}
	var result strings.Builder
	result.Grow(len(data) / 2)
	for offset := 2; offset < len(data); offset += 2 {
		unit := binary.LittleEndian.Uint16(data[offset:])
		switch {
		case unit >= 0xd800 && unit <= 0xdbff:
			if offset+3 >= len(data) {
				return "", fmt.Errorf("truncated UTF-16 surrogate")
			}
			next := binary.LittleEndian.Uint16(data[offset+2:])
			if next < 0xdc00 || next > 0xdfff {
				return "", fmt.Errorf("invalid UTF-16 surrogate pair")
			}
			result.WriteRune(utf16.DecodeRune(rune(unit), rune(next)))
			offset += 2
		case unit >= 0xdc00 && unit <= 0xdfff:
			return "", fmt.Errorf("unpaired UTF-16 surrogate")
		case unit == 0:
			return "", fmt.Errorf("UTF-16 input contains NUL")
		default:
			result.WriteRune(rune(unit))
		}
	}
	return result.String(), nil
}

// Open validates and loads a generated dictionary. It never interprets native
// struct layouts or indexes an untrusted offset.
func Open(dictionary []byte) (*Decoder, error) {
	if len(dictionary) == 0 || len(dictionary) > MaxCompressedDictionary {
		return nil, fmt.Errorf("invalid compressed Pinyin dictionary size")
	}
	zipper, err := gzip.NewReader(bytes.NewReader(dictionary))
	if err != nil {
		return nil, fmt.Errorf("Pinyin dictionary gzip: %w", err)
	}
	plain, readErr := io.ReadAll(io.LimitReader(zipper, MaxUncompressedDictionary+1))
	closeErr := zipper.Close()
	if readErr != nil {
		return nil, fmt.Errorf("Pinyin dictionary: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("Pinyin dictionary: %w", closeErr)
	}
	if len(plain) > MaxUncompressedDictionary {
		return nil, fmt.Errorf("Pinyin dictionary expands beyond %d bytes", MaxUncompressedDictionary)
	}
	scanner := bufio.NewScanner(bytes.NewReader(plain))
	scanner.Buffer(make([]byte, 256), maxRecordBytes)
	if !scanner.Scan() {
		return nil, fmt.Errorf("Pinyin dictionary is empty")
	}
	header := strings.Split(scanner.Text(), "\t")
	if len(header) != 2 || header[0] != dictionaryHeader {
		return nil, fmt.Errorf("unsupported Pinyin dictionary format")
	}
	wanted, err := strconv.Atoi(header[1])
	if err != nil || wanted < 1 || wanted > MaxEntries {
		return nil, fmt.Errorf("invalid Pinyin dictionary entry count")
	}
	decoder := &Decoder{entries: make([][]entry, maxSyllables+1), syllables: make(map[string]struct{}), prefixes: make(map[string]struct{})}
	count := 0
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid Pinyin dictionary record %d", count+1)
		}
		syllables := strings.Split(fields[2], "'")
		if len(syllables) < 1 || len(syllables) > maxSyllables || !validCandidate(fields[0], len(syllables)) {
			return nil, fmt.Errorf("invalid Pinyin candidate at record %d", count+1)
		}
		for _, syllable := range syllables {
			if !validSyllable(syllable) {
				return nil, fmt.Errorf("invalid Pinyin spelling at record %d", count+1)
			}
			decoder.syllables[syllable] = struct{}{}
			for length := 1; length < len(syllable); length++ {
				decoder.prefixes[syllable[:length]] = struct{}{}
			}
		}
		frequency, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || frequency <= 0 || math.IsInf(frequency, 0) || math.IsNaN(frequency) {
			return nil, fmt.Errorf("invalid Pinyin frequency at record %d", count+1)
		}
		decoder.entries[len(syllables)] = append(decoder.entries[len(syllables)], entry{fields[0], frequency, syllables})
		count++
		if count > wanted || count > MaxEntries {
			return nil, fmt.Errorf("Pinyin dictionary contains too many entries")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if count != wanted {
		return nil, fmt.Errorf("Pinyin dictionary entry count is %d, expected %d", count, wanted)
	}
	return decoder, nil
}

func validCandidate(text string, syllables int) bool {
	if text == "" || !utf8.ValidString(text) || syllables < 1 || syllables > maxSyllables || utf8.RuneCountInString(text) != syllables || utf8.RuneCountInString(text) > maxCandidateRunes {
		return false
	}
	for _, character := range text {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func validSyllable(syllable string) bool {
	if len(syllable) < 1 || len(syllable) > maxSyllableBytes {
		return false
	}
	for _, letter := range []byte(syllable) {
		if letter < 'a' || letter > 'z' {
			return false
		}
	}
	return true
}
