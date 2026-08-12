package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// makeMp3Bytes encodes two seconds of 44.1 kHz silence with ffmpeg, returning
// (data, available). Callers skip when ffmpeg is missing.
func makeMp3Bytes(t *testing.T) ([]byte, bool) {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, false
	}
	f, err := os.CreateTemp("", "test.mp3")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	cmd := exec.Command(ff, "-loglevel", "error", "-y", "-f", "lavfi",
		"-i", "anullsrc=r=44100:cl=mono", "-t", "2", "-q:a", "9", "-f", "mp3", name)
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data, true
}

// makeAnimatedWebpBytes generates a 64x64 1s 10fps animated WebP with ffmpeg,
// returning (data, available). Callers skip when ffmpeg is missing.
func makeAnimatedWebpBytes(t *testing.T) ([]byte, bool) {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, false
	}
	f, err := os.CreateTemp("", "anim.webp")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	cmd := exec.Command(ff, "-loglevel", "error", "-y", "-f", "lavfi",
		"-i", "testsrc=duration=1:size=64x64:rate=10", "-loop", "0", "-f", "webp", name)
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data, true
}

// makeID3v2 builds an ID3v2.3 tag with the given title/artist/album frames.
func makeID3v2(title, artist, album string) []byte {
	var frames []byte
	frames = append(frames, makeID3Frame("TIT2", title)...)
	frames = append(frames, makeID3Frame("TPE1", artist)...)
	frames = append(frames, makeID3Frame("TALB", album)...)
	size := len(frames)
	body := make([]byte, 10+size)
	copy(body, "ID3")
	body[3] = 3 // v2.3
	body[6] = byte(size>>21 & 0x7f)
	body[7] = byte(size>>14 & 0x7f)
	body[8] = byte(size>>7 & 0x7f)
	body[9] = byte(size & 0x7f)
	copy(body[10:], frames)
	return body
}

func makeID3Frame(id, text string) []byte {
	data := append([]byte{0}, []byte(text)...) // encoding 0 (ISO-8859-1)
	n := len(data)
	f := make([]byte, 10+n)
	copy(f, id)
	f[4] = byte(n >> 24)
	f[5] = byte(n >> 16)
	f[6] = byte(n >> 8)
	f[7] = byte(n)
	copy(f[10:], data)
	return f
}

func TestParseID3v2(t *testing.T) {
	tag := makeID3v2("My Song", "The Artist", "Album X")
	title, artist, album, year, size, ok := parseID3v2(bytes.NewReader(tag))
	if !ok {
		t.Fatal("ID3v2 not detected")
	}
	if title != "My Song" || artist != "The Artist" || album != "Album X" {
		t.Fatalf("tags = %q / %q / %q", title, artist, album)
	}
	if year != "" {
		t.Fatalf("unexpected year %q", year)
	}
	if size != len(tag) {
		t.Fatalf("tag size %d != %d", size, len(tag))
	}
}

func TestParseID3v2NotPresent(t *testing.T) {
	if _, _, _, _, _, ok := parseID3v2(bytes.NewReader([]byte("not a tag"))); ok {
		t.Fatal("non-ID3 data reported as a tag")
	}
}

func TestDecodeID3TextUTF16(t *testing.T) {
	// encoding 1, UTF-16 LE with BOM
	le := []byte{0x01, 0xff, 0xfe, 'h', 0x00, 'i', 0x00, 0x00, 0x00}
	if got := decodeID3Text(le); got != "hi" {
		t.Fatalf("utf-16le decode = %q", got)
	}
	be := []byte{0x02, 0x00, 'h', 0x00, 'i', 0x00, 0x00}
	if got := decodeID3Text(be); got != "hi" {
		t.Fatalf("utf-16be decode = %q", got)
	}
	// encoding 3, UTF-8
	u8 := []byte{0x03, 'h', 0xc3, 0xa9, 0x00}
	if got := decodeID3Text(u8); got != "hé" {
		t.Fatalf("utf-8 decode = %q", got)
	}
}

func TestParseID3v1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.mp3")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("MPEGFRAME!")) // 10 bytes of audio, then the tag
	var tag [128]byte
	copy(tag[:3], "TAG")
	copy(tag[3:33], "Old Title")
	copy(tag[33:63], "Old Artist")
	copy(tag[63:93], "Old Album")
	copy(tag[93:97], "1999")
	f.Write(tag[:])
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	title, artist, album, year, ok := parseID3v1(r, 128+10)
	if !ok {
		t.Fatal("ID3v1 not detected")
	}
	if title != "Old Title" || artist != "Old Artist" || album != "Old Album" || year != "1999" {
		t.Fatalf("id3v1 = %q / %q / %q / %q", title, artist, album, year)
	}
}

func TestDurationString(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "-"},
		{2 * time.Second, "00:02"},
		{65 * time.Second, "01:05"},
		{2 * time.Minute, "02:00"},
		{3*time.Minute + 9*time.Second, "03:09"},
	}
	for _, c := range cases {
		if got := (mp3Meta{duration: c.d}).durationString(); got != c.want {
			t.Errorf("durationString(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestReadMp3Meta(t *testing.T) {
	src, ok := makeMp3Bytes(t)
	if !ok {
		t.Skip("ffmpeg not available")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "song.mp3")
	tag := makeID3v2("My Song", "The Artist", "Album X")
	if err := os.WriteFile(path, append(tag, src...), 0644); err != nil {
		t.Fatal(err)
	}
	meta, err := readMp3Meta(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.title != "My Song" || meta.artist != "The Artist" || meta.album != "Album X" {
		t.Fatalf("tags = %q / %q / %q", meta.title, meta.artist, meta.album)
	}
	if meta.sampleRate != 44100 {
		t.Fatalf("sampleRate = %d, want 44100", meta.sampleRate)
	}
	if meta.duration < time.Second || meta.duration > 3*time.Second {
		t.Fatalf("duration = %v, want ~2s", meta.duration)
	}
	if meta.bitrateKbps <= 0 {
		t.Fatalf("bitrate = %d, want > 0", meta.bitrateKbps)
	}
}
