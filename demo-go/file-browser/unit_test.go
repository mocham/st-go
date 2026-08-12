package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestDisplayTextSanitizes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain.txt", "plain.txt"},
		{"with space.txt", "with\\ space.txt"},
		{"a'quote", "a\\'quote"},
		{"back\\slash", "back\\\\slash"},
		{"tab\there", "$'tab\\there'"},
		{"line\nbreak", "$'line\\nbreak'"},
		{"esc\x1b[31mred", "$'esc\\E[31mred'"},
		{"ctl\x07bell", "$'ctl\\abell'"},
		{"del\x7fdel", "$'del\\177del'"},
		{"日本語.txt", "日本語.txt"},
		{"a[b]c", "a\\[b\\]c"},
	}
	for _, c := range cases {
		if got := displayText(c.in); got != c.want {
			t.Errorf("displayText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range cases {
		if got := displayText(c.in); containsANSI(got) {
			t.Errorf("displayText(%q) leaked an ESC sequence: %q", c.in, got)
		}
	}
}

func containsANSI(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			return true
		}
	}
	return false
}

func TestShorten(t *testing.T) {
	if got := shorten("abcdef", 4); got != "a..." {
		t.Errorf("shorten = %q, want a...", got)
	}
	if got := shorten("abc", 4); got != "abc" {
		t.Errorf("shorten = %q, want abc", got)
	}
	if got := shorten("abcdef", 2); got != "ab" {
		t.Errorf("shorten = %q, want ab", got)
	}
	if got := shorten("abcdef", 0); got != "" {
		t.Errorf("shorten = %q, want empty", got)
	}
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{2048, "2 KiB"},
		{1048576, "1 MiB"},
		{1073741824, "1 GiB"},
	}
	for _, c := range cases {
		if got := humanSize(c.n); got != c.want {
			t.Errorf("humanSize(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestModeString(t *testing.T) {
	if got := modeString(0); got != "----------" {
		t.Errorf("modeString(0) = %q", got)
	}
}

func TestParseSubst(t *testing.T) {
	cases := []struct {
		in          string
		ok          bool
		old, new    string
		global      bool
	}{
		{":s/txt/md/", true, "txt", "md", false},
		{":s/txt/md/g", true, "txt", "md", true},
		{":s/-/X/", true, "-", "X", false},
		{":s/delete-this//", true, "delete-this", "", false},
		{":s/bad", false, "", "", false},
		{":s//new/", false, "", "", false},
		{":s/a/b/q", false, "", "", false},
		{"ls", false, "", "", false},
		{"", false, "", "", false},
	}
	for _, c := range cases {
		b := &Browser{promptBuffer: []byte(c.in)}
		ok := b.parseSubst()
		if ok != c.ok {
			t.Errorf("parseSubst(%q) ok=%v want %v", c.in, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if b.cmdOld != c.old || b.cmdNew != c.new || b.cmdGlobal != c.global {
			t.Errorf("parseSubst(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, b.cmdOld, b.cmdNew, b.cmdGlobal, c.old, c.new, c.global)
		}
	}
}

func TestUpdateCommandPreview(t *testing.T) {
	dir := "/d"
	b := &Browser{
		dir:          dir,
		files:        []string{dir + "/..", dir + "/a-b.txt", dir + "/c.txt"},
		promptBuffer: []byte(":s/b/XY/"),
	}
	b.updateCommandPreview()
	if len(b.cmdAffected) != 1 || b.cmdAffected[0] != 1 {
		t.Fatalf("cmdAffected = %v, want [1]", b.cmdAffected)
	}
	if len(b.cmdNewNames) != 1 || b.cmdNewNames[0] != "a-XY.txt" {
		t.Fatalf("cmdNewNames = %v, want [a-XY.txt]", b.cmdNewNames)
	}
	if b.cmdMessage == "" {
		t.Fatal("cmdMessage should announce the rename count")
	}

	b = &Browser{dir: dir, files: []string{dir + "/..", dir + "/a.txt"}, promptBuffer: []byte("ls")}
	b.updateCommandPreview()
	if len(b.cmdAffected) != 0 {
		t.Fatalf("non-rename prompt produced affected rows: %v", b.cmdAffected)
	}
}

func TestBaseName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/a/b.txt", "b.txt"},
		{"/a/b/", "b"},
		{"/", "/"},
		{"..", ".."},
		{"/tmp/x/..", ".."},
	}
	for _, c := range cases {
		if got := baseName(c.in); got != c.want {
			t.Errorf("baseName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsAnimatedWebpHeader(t *testing.T) {
	mk := func(flag byte) []byte {
		h := make([]byte, 30)
		copy(h[0:4], "RIFF")
		copy(h[8:12], "WEBP")
		copy(h[12:16], "VP8X")
		h[20] = flag
		return h
	}
	if !isAnimatedWebpHeader(mk(0x02)) {
		t.Fatal("animation flag 0x02 not detected")
	}
	if isAnimatedWebpHeader(mk(0x00)) {
		t.Fatal("animation flag absent should not be detected")
	}
	if isAnimatedWebpHeader([]byte("not a webp")) {
		t.Fatal("non-webp header detected as animated")
	}
	// static WebP (VP8 fourcc, not VP8X)
	s := []byte("RIFF....WEBPVP8 ")
	if isAnimatedWebpHeader(s) {
		t.Fatal("VP8 static header detected as animated")
	}
}

func TestHasSuffixAny(t *testing.T) {
	if hasSuffixAny("a.PDF", ".pdf") {
		t.Error("hasSuffixAny should be case sensitive on exact suffixes")
	}
	if !hasSuffixAny("a.pdf", ".pdf") {
		t.Error("hasSuffixAny(a.pdf, .pdf) = false")
	}
	if hasSuffixAny("a.pdf", ".txt", ".md") {
		t.Error("hasSuffixAny matched the wrong suffix")
	}
}

func TestIsArchive(t *testing.T) {
	b := &Browser{}
	for _, n := range []string{"a.zip", "a.rar", "a.cbz", "a.CBR", "upper.ZIP"} {
		if !b.isArchive(n) {
			t.Errorf("isArchive(%s) = false", n)
		}
	}
	for _, n := range []string{"a.txt", "a.tar", "a.tar.gz", "a.7z", "a.bz2"} {
		if b.isArchive(n) {
			t.Errorf("isArchive(%s) = true, want false", n)
		}
	}
}

func TestFileTypeArchiveIcons(t *testing.T) {
	b := &Browser{dir: "/d"}
	for _, n := range []string{"a.zip", "a.cbz", "a.cbr", "a.rar"} {
		if got := b.fileType("/d/" + n); got != "archive" {
			t.Errorf("fileType(%s) = %q, want archive", n, got)
		}
	}
}

func TestExtractZipEntryUnit(t *testing.T) {
	src := t.TempDir()
	zpath := filepath.Join(src, "a.zip")
	makeTestZip(t, zpath, map[string]string{
		"top.txt":       "top",
		"dir/nested.go": "package main\n",
	})
	zr, err := zip.OpenReader(zpath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	dest := t.TempDir()
	p, err := extractZipEntry(zr, "top.txt", dest)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(p); err != nil || string(data) != "top" {
		t.Fatalf("top.txt content wrong: %v %q", err, data)
	}
	// a nested entry keeps its subpath when materialized
	if _, err := extractZipEntry(zr, "dir/nested.go", dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "dir", "nested.go")); err != nil {
		t.Fatalf("nested entry not materialized: %v", err)
	}
}

func TestExtractZipEntryTraversal(t *testing.T) {
	src := t.TempDir()
	zpath := filepath.Join(src, "evil.zip")
	makeTestZip(t, zpath, map[string]string{"../escaped.txt": "evil", "ok.txt": "fine"})
	zr, err := zip.OpenReader(zpath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	dest := t.TempDir()
	if _, err := extractZipEntry(zr, "../escaped.txt", dest); err == nil {
		t.Fatal("traversal entry should be rejected")
	}
	if _, err := os.Stat(filepath.Join(src, "escaped.txt")); err == nil {
		t.Fatal("path traversal wrote outside the destination")
	}
	if _, err := extractZipEntry(zr, "ok.txt", dest); err != nil {
		t.Fatalf("safe entry not materialized: %v", err)
	}
}

// TestMaterializeEntryLazy proves extraction is lazy: materializing one entry
// must not extract the others.
func TestMaterializeEntryLazy(t *testing.T) {
	src := t.TempDir()
	zpath := filepath.Join(src, "a.zip")
	makeTestZip(t, zpath, map[string]string{"one.txt": "1", "two.txt": "2"})
	b := &Browser{cacheDir: t.TempDir()}
	ctx := &archiveCtx{isZip: true, extracted: map[string]string{}}
	zr, err := zip.OpenReader(zpath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	ctx.zip = zr
	p, err := b.materializeEntry(ctx, "one.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("materialized entry missing: %v", err)
	}
	entries, err := os.ReadDir(ctx.tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one materialized entry, got %d", len(entries))
	}
	if _, err := os.Stat(filepath.Join(ctx.tmpDir, "two.txt")); err == nil {
		t.Fatal("two.txt was extracted eagerly")
	}
	// a second materialize call reuses the cache
	p2, err := b.materializeEntry(ctx, "one.txt")
	if err != nil || p2 != p {
		t.Fatalf("materialize did not reuse the cache: %q vs %q err=%v", p2, p, err)
	}
}
