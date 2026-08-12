package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/hajimehoshi/go-mp3"
)

// mp3Meta holds the information the browser itself decodes from an mp3 file:
// ID3 tags plus technical details from the pure-Go decoder.
type mp3Meta struct {
	title, artist, album, year string
	duration                   time.Duration
	sampleRate                 int
	bitrateKbps                int
}

func (m mp3Meta) durationString() string {
	if m.duration <= 0 {
		return "-"
	}
	d := m.duration.Round(time.Second)
	return fmt.Sprintf("%02d:%02d", int(d/time.Minute), int(d%time.Minute)/int(time.Second))
}

// readMp3Meta decodes an mp3 with the pure-Go decoder and reads its ID3 tags.
// The full stream is scanned for the duration/bitrate, so this is only run
// when an mp3 is previewed or opened (lazily, like archive extraction).
func readMp3Meta(path string) (mp3Meta, error) {
	meta := mp3Meta{}
	f, err := os.Open(path)
	if err != nil {
		return meta, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return meta, err
	}
	size := st.Size()

	// ID3v2 tags live at the head of the file.
	id3size := 0
	if title, artist, album, year, n, ok := parseID3v2(f); ok {
		meta.title, meta.artist, meta.album, meta.year = title, artist, album, year
		id3size = n
	}
	// ID3v1 tags live in the final 128 bytes.
	if meta.title == "" && meta.artist == "" && meta.album == "" {
		if title, artist, album, year, ok := parseID3v1(f, size); ok {
			meta.title, meta.artist, meta.album, meta.year = title, artist, album, year
		}
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return meta, err
	}
	dec, err := mp3.NewDecoder(f)
	if err != nil {
		return meta, err
	}
	meta.sampleRate = dec.SampleRate()
	// go-mp3 emits 16-bit stereo: 4 bytes per sample.
	if l := dec.Length(); l > 0 && meta.sampleRate > 0 {
		meta.duration = time.Duration(l/(4*int64(meta.sampleRate))) * time.Second
		if meta.duration > 0 {
			audioBytes := size - int64(id3size)
			if audioBytes < 0 {
				audioBytes = size
			}
			meta.bitrateKbps = int(audioBytes*8/1000) / int(meta.duration/time.Second)
		}
	}
	return meta, nil
}

// streamMp3 decodes path with go-mp3 and writes the 16-bit stereo PCM to the
// ALSA player until the file ends or playback is stopped. It owns the player
// handle and closes it exactly once. Runs in a background goroutine.
func (b *Browser) streamMp3(path string, player *alsaPlayer) {
	defer player.close()
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	dec, err := mp3.NewDecoder(f)
	if err != nil {
		return
	}
	buf := make([]byte, 8192)
	for {
		select {
		case <-b.playbackStop:
			return
		default:
		}
		n, err := dec.Read(buf)
		if n > 0 {
			if serr := player.send(buf[:n]); serr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// stopPlayback signals the streaming goroutine to stop. The goroutine owns the
// ALSA handle and closes it.
func (b *Browser) stopPlayback() {
	if b.playbackStop != nil {
		close(b.playbackStop)
		b.playbackStop = nil
	}
	b.playback = nil
}

// parseID3v2 reads an ID3v2 tag from the start of r and returns the title,
// artist, album, year, the total tag size (header + body), and whether a tag
// was present.
func parseID3v2(r io.Reader) (title, artist, album, year string, size int, ok bool) {
	hdr := make([]byte, 10)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return "", "", "", "", 0, false
	}
	if string(hdr[0:3]) != "ID3" {
		return "", "", "", "", 0, false
	}
	ver := hdr[3]
	bodyLen := synchsafe32(hdr[6:10])
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", "", "", "", 0, false
	}
	i := 0
	for {
		id := id3FrameID(ver, body, i)
		if id == "" {
			break
		}
		headerLen := 6
		if ver != 2 {
			headerLen = 10
		}
		frameLen := id3FrameSize(ver, body, i+len(id))
		if frameLen <= 0 || i+headerLen+frameLen > len(body) {
			break
		}
		data := body[i+headerLen : i+headerLen+frameLen]
		i += headerLen + frameLen
		switch id {
		case "TIT2", "TT2":
			title = decodeID3Text(data)
		case "TPE1", "TP1":
			artist = decodeID3Text(data)
		case "TALB", "TAL":
			album = decodeID3Text(data)
		case "TYER", "TYE", "TDRC":
			year = decodeID3Text(data)
		}
	}
	return title, artist, album, year, 10 + bodyLen, true
}

func id3FrameID(ver byte, body []byte, i int) string {
	if ver == 2 {
		if i+3 > len(body) {
			return ""
		}
		id := string(body[i : i+3])
		if id[0] == 0 {
			return ""
		}
		return id
	}
	if i+4 > len(body) {
		return ""
	}
	id := string(body[i : i+4])
	if id[0] == 0 {
		return ""
	}
	return id
}

func id3FrameSize(ver byte, body []byte, at int) int {
	if ver == 2 {
		if at+3 > len(body) {
			return 0
		}
		return int(body[at])<<16 | int(body[at+1])<<8 | int(body[at+2])
	}
	if at+4 > len(body) {
		return 0
	}
	if ver == 4 {
		return synchsafe32(body[at : at+4])
	}
	return int(body[at])<<24 | int(body[at+1])<<16 | int(body[at+2])<<8 | int(body[at+3])
}

// synchsafe32 decodes a 4-byte ID3 synchsafe integer (7 significant bits per
// byte).
func synchsafe32(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	return int(b[0]&0x7f)<<21 | int(b[1]&0x7f)<<14 | int(b[2]&0x7f)<<7 | int(b[3]&0x7f)
}

// decodeID3Text decodes a text frame body: encoding byte followed by text.
func decodeID3Text(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	enc := data[0]
	text := data[1:]
	switch enc {
	case 1: // UTF-16 with BOM
		if len(text) >= 2 && text[0] == 0xfe && text[1] == 0xff {
			return decodeUTF16(text[2:], true)
		}
		if len(text) >= 2 && text[0] == 0xff && text[1] == 0xfe {
			return decodeUTF16(text[2:], false)
		}
		return decodeUTF16(text, false)
	case 2: // UTF-16BE
		return decodeUTF16(text, true)
	case 3: // UTF-8
		return strings.TrimRight(string(text), "\x00")
	default: // ISO-8859-1
		return strings.TrimRight(string(text), "\x00")
	}
}

func decodeUTF16(b []byte, bigEndian bool) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		var u uint16
		if bigEndian {
			u = uint16(b[i])<<8 | uint16(b[i+1])
		} else {
			u = uint16(b[i+1])<<8 | uint16(b[i])
		}
		units = append(units, u)
	}
	// Trim a trailing U+0000 terminator.
	for len(units) > 0 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	return string(utf16.Decode(units))
}

// parseID3v1 reads a trailing 128-byte ID3v1 tag.
func parseID3v1(f *os.File, size int64) (title, artist, album, year string, ok bool) {
	if size < 128 {
		return "", "", "", "", false
	}
	buf := make([]byte, 128)
	if _, err := f.ReadAt(buf, size-128); err != nil {
		return "", "", "", "", false
	}
	if string(buf[0:3]) != "TAG" {
		return "", "", "", "", false
	}
	trim := func(b []byte) string { return strings.TrimRight(string(b), "\x00 \t") }
	return trim(buf[3:33]), trim(buf[33:63]), trim(buf[63:93]), trim(buf[93:97]), true
}
