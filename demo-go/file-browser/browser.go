// Package main implements a Go clone of demo/file-browser.sh.
//
// It preserves the execution logic of the Bash browser: the same terminal
// layout, the same key/mouse handling, the same path and command prompts, the
// same vim-style rename substitution, and the same :help manual, all rendered
// with the same ANSI/VT and DCS sequences.
package main

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Terminal styles shared with the shell browser.
const (
	resetStyle    = "\x1b[0m"
	dimStyle      = "\x1b[2m"
	headerStyle   = "\x1b[38;5;231m\x1b[48;5;24m\x1b[1m"
	selectedStyle = "\x1b[38;5;231m\x1b[48;5;31m\x1b[1m"
	dirStyle      = "\x1b[38;5;81m\x1b[1m"
	imageStyle    = "\x1b[38;5;213m"
	pdfStyle      = "\x1b[38;5;203m"
	infoStyle     = "\x1b[38;5;110m"
	statusStyle   = "\x1b[38;5;254m\x1b[48;5;236m"
	renameHL      = "\x1b[38;5;16m\x1b[48;5;220m\x1b[1m"
)

const pathPopupMax = 8

// archiveCtx is one level of archive browsing. While an archive is open the
// browser lists its entries virtually (metadata only) and extracts a file to
// /tmp lazily, only when it is opened or previewed.
type archiveCtx struct {
	file   string // archive path (real)
	rel    string // virtual directory inside the archive ("" = root)
	isZip  bool   // zip-family (zip/cbz) vs rar-family (rar/cbr)
	zip    *zip.ReadCloser
	names  []string // full virtual entry names (dirs keep no trailing slash)
	extracted map[string]string // virtual entry -> materialized /tmp path
	tmpDir string
}

// Browser is the file browser state machine. It mirrors file-browser.sh.
type Browser struct {
	in  *input
	out *bufio.Writer
	fd  int // stdin/stdout fd used for termios and window size

	// layout (cells, ANSI one-based)
	rows, cols           int
	listW, listX         int
	previewW             int
	listTop, listBottom  int
	visible              int
	compact              bool

	// browsing state
	dir      string
	dirLabel string // path shown in the header (the archive while inside it)
	files    []string
	idx      int
	viewTop  int
	page     int
	status   string
	showHidden bool
	openCmd  string

	// lazy archive browsing: archiveStack holds each open archive level;
	// archiveReturn* records the real directory to return to when the stack
	// empties.
	archiveStack        []*archiveCtx
	archiveReturnDir    string
	archiveReturnEntry  string

	// preview state
	previewWasGraphic bool
	previewFullRedraw bool
	atlasClean        bool

	// info pane
	infoName, infoKind, infoSize, infoMode, infoTime string

	// click state
	lastClickIdx int
	lastClickMs  int64
	doubleClickMS int64

	// modal prompt state
	promptActive bool
	promptAbort  bool
	promptMode   string // "path" or "cmd"
	promptBuffer []byte
	promptCursor int
	pathMatches  []string
	pathMatchIdx int
	pathMatchTop int
	pathMessage  string
	cmdMessage   string
	cmdAffected  []int
	cmdNewNames  []string
	cmdPrevAffected []int
	cmdOld, cmdNew string
	cmdGlobal    bool

	// manual state
	manualActive bool
	manualPage   int
	manualTotal  int
	manualLines  []string

	// mp3 state
	mp3Active    bool
	mp3Cache     map[string]mp3Meta
	playback     *alsaPlayer // non-nil while the mp3 viewer is playing
	playbackStop chan struct{}
	playbackErr  error

	// runtime
	resized  bool
	running  bool
	exitCode int
	cleaned  bool

	// resources
	cacheDir     string
	previewLink  string
	geometryTag  string
	geometryToken string
	icons        map[string]string

	sigCh chan os.Signal
}

var iconDefaults = map[string]string{
	"PARENT":     "↑",
	"DIRECTORY":  "▸",
	"SYMLINK":    "↗",
	"IMAGE":      "▣",
	"PDF":        "▤",
	"TEXT":       "≡",
	"ARCHIVE":    "▦",
	"AUDIO":      "♪",
	"VIDEO":      "▶",
	"CODE":       "λ",
	"CONFIG":     "⚙",
	"EXECUTABLE": "◆",
	"DEFAULT":    "·",
}

func iconValue(key string) string {
	if v, ok := os.LookupEnv("ST_FILE_BROWSER_ICON_" + key); ok {
		return v
	}
	if v, ok := os.LookupEnv("ST_GO_FILE_BROWSER_ICON_" + key); ok {
		return v
	}
	return iconDefaults[key]
}

// defaultOpenCmd mirrors the shell default opener: text/source/config formats
// open in the running terminal's vim; everything else uses xdg-open.
const defaultOpenCmd = `
editor=${ST_GO_EXECUTABLE:-st}
case ${FILE,,} in
  *.txt|*.md|*.markdown|*.html|*.htm|*.css|*.tex|*.bib|*.bbl|*.py|*.c|*.h|*.cc|*.cpp|*.cxx|*.go|*.rs|*.java|*.js|*.jsx|*.ts|*.tsx|*.sh|*.bash|*.zsh|*.yml|*.yaml|*.json|*.toml|*.ini|*.conf|*.xml|*.csv|*.log|*.rst)
    "$editor" vim "$FILE"
    ;;
  *)
    bar external "$FILE"
    ;;
esac`

func defaultOpenCommand() string {
	if v, ok := os.LookupEnv("ST_FILE_BROWSER_OPEN"); ok {
		return v
	}
	return defaultOpenCmd
}

func parseShowHidden() bool {
	v, ok := os.LookupEnv("ST_FILE_BROWSER_HIDDEN")
	if !ok {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func parseDoubleClickMS() int64 {
	v, ok := os.LookupEnv("ST_FILE_BROWSER_DOUBLE_CLICK_MS")
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil || !ok {
		ms = 350
	}
	if ms < 50 {
		ms = 50
	}
	if ms > 2000 {
		ms = 2000
	}
	return ms
}

// setup initializes runtime state after argument parsing.
func (b *Browser) setup() {
	b.icons = map[string]string{
		"parent":     iconValue("PARENT"),
		"directory":  iconValue("DIRECTORY"),
		"symlink":    iconValue("SYMLINK"),
		"image":      iconValue("IMAGE"),
		"pdf":        iconValue("PDF"),
		"text":       iconValue("TEXT"),
		"archive":    iconValue("ARCHIVE"),
		"audio":      iconValue("AUDIO"),
		"video":      iconValue("VIDEO"),
		"code":       iconValue("CODE"),
		"config":     iconValue("CONFIG"),
		"executable": iconValue("EXECUTABLE"),
		"default":    iconValue("DEFAULT"),
	}
	b.listTop = 8
	b.idx = 0
	b.viewTop = 0
	b.page = 1
	b.status = "Ready"
	b.atlasClean = true
	b.lastClickIdx = -1
	b.doubleClickMS = parseDoubleClickMS()
	b.geometryToken = os.Getenv("ST_GO_GEOMETRY_TOKEN")
	b.dirLabel = b.dir
	b.mp3Cache = map[string]mp3Meta{}
	// Materialize the embedded alsa.conf before any libasound call.
	if b.cacheDir != "" {
		_ = writeALSAConfig(b.cacheDir)
	}
	if b.cacheDir == "" {
		b.cacheDir, _ = os.MkdirTemp("", "st-go-browser.")
	}
	b.previewLink = filepath.Join(b.cacheDir, "content")
	b.geometryTag = fmt.Sprintf("file-browser-%d", os.Getpid())
}

// start enters the alternate screen, remembers geometry, and renders.
func (b *Browser) start() {
	b.terminalSize()
	b.buildList()
	fmt.Fprint(b.out, "\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h")
	b.windowDCS("remember " + b.geometryTag)
	b.renderAll()
}

// cleanup restores the terminal, forgets geometry, and removes the cache.
func (b *Browser) cleanup() {
	if b.cleaned {
		return
	}
	b.cleaned = true
	for _, ctx := range b.archiveStack {
		if ctx.zip != nil {
			ctx.zip.Close()
		}
	}
	b.windowDCS("forget " + b.geometryTag)
	fmt.Fprint(b.out, "\x1b[?1006l\x1b[?1000l\x1b[?25h\x1b[0m\x1b[?1049l")
	b.flush()
	b.restoreTermios()
	os.RemoveAll(b.cacheDir)
}

func (b *Browser) flush() { b.out.Flush() }

func (b *Browser) dcs(s string) { fmt.Fprintf(b.out, "\x1bP%s\x1b\\", s) }

// paintStop/paintResume wrap a UI frame in the terminal's synchronized-output
// protocol (DECSET/DECRST 2026) so the whole render is painted atomically with
// a single flush instead of per-sequence.
func (b *Browser) paintStop()   { fmt.Fprint(b.out, "\x1b[?2026h") }
func (b *Browser) paintResume() { fmt.Fprint(b.out, "\x1b[?2026l") }

func (b *Browser) windowDCS(args string) {
	if b.geometryToken != "" {
		b.dcs("window auth " + b.geometryToken + " " + args)
	} else {
		b.dcs("window " + args)
	}
}

func (b *Browser) cup(row, col int) { fmt.Fprintf(b.out, "\x1b[%d;%dH", row, col) }

// terminalSize reads the live window size like `stty size`.
func (b *Browser) terminalSize() {
	rows, cols := 24, 80
	if ws, err := unix.IoctlGetWinsize(b.fd, unix.TIOCGWINSZ); err == nil {
		if ws.Row > 0 {
			rows = int(ws.Row)
		}
		if ws.Col > 0 {
			cols = int(ws.Col)
		}
	}
	b.rows, b.cols = rows, cols
	b.compact = rows < 12 || cols < 44
	if b.compact {
		return
	}
	listW := 34
	if v, err := strconv.Atoi(os.Getenv("ST_FILE_BROWSER_LIST_WIDTH")); err == nil {
		listW = v
	}
	if listW < 24 {
		listW = 24
	}
	if listW > cols/2 {
		listW = cols / 2
	}
	b.listW = listW
	b.listX = cols - listW + 1
	b.previewW = b.listX - 2
	b.listBottom = rows - 2
	b.visible = b.listBottom - b.listTop + 1
	if b.visible < 1 {
		b.visible = 1
	}
}

// buildList reads the current directory into files: parent first, then sorted
// directories, then sorted regular files (mirrors the shell glob ordering).
// Inside an archive it lists the archive's virtual entries instead.
func (b *Browser) buildList() {
	if len(b.archiveStack) > 0 {
		b.buildArchiveList(b.currentArchive())
		return
	}
	b.files = nil
	if b.dir != "/" {
		b.files = append(b.files, b.dir+"/..")
	}
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		entries = nil
	}
	var dirs, regular []string
	for _, e := range entries {
		name := e.Name()
		if !b.showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(b.dir, name)
		if isDir(full) {
			dirs = append(dirs, full)
		} else {
			regular = append(regular, full)
		}
	}
	b.files = append(b.files, dirs...)
	b.files = append(b.files, regular...)
	if len(b.files) == 0 {
		b.idx = -1
	}
}

// buildArchiveList lists the current virtual directory of an open archive:
// the ".." parent, then sorted subdirectories (trailing "/"), then sorted
// files. Only the archive's metadata is used; no contents are extracted.
func (b *Browser) buildArchiveList(ctx *archiveCtx) {
	prefix := ""
	if ctx.rel != "" {
		prefix = ctx.rel + "/"
	}
	b.files = []string{".."}
	dirSet := map[string]bool{}
	var files []string
	for _, n := range ctx.names {
		if n == "" || n == ctx.rel {
			continue
		}
		if prefix != "" && !strings.HasPrefix(n, prefix) {
			continue
		}
		rest := strings.TrimPrefix(n, prefix)
		if rest == "" {
			continue
		}
		if i := strings.Index(rest, "/"); i >= 0 {
			dirSet[rest[:i]] = true
		} else {
			files = append(files, rest)
		}
	}
	var dirs []string
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	sort.Strings(files)
	for _, d := range dirs {
		b.files = append(b.files, d+"/")
	}
	b.files = append(b.files, files...)
}

func (b *Browser) ensureVisible() {
	count := len(b.files)
	if count == 0 {
		b.idx = -1
		b.viewTop = 0
		return
	}
	if b.idx < 0 {
		b.idx = 0
	}
	if b.idx >= count {
		b.idx = count - 1
	}
	if b.idx < b.viewTop {
		b.viewTop = b.idx
	}
	if b.idx >= b.viewTop+b.visible {
		b.viewTop = b.idx - b.visible + 1
	}
	maxTop := count - b.visible
	if maxTop < 0 {
		maxTop = 0
	}
	if b.viewTop > maxTop {
		b.viewTop = maxTop
	}
	if b.viewTop < 0 {
		b.viewTop = 0
	}
}

func (b *Browser) updateInfo() {
	if b.idx < 0 || b.idx >= len(b.files) {
		b.infoName = "(empty directory)"
		b.infoKind = "directory"
		b.infoSize = "-"
		b.infoMode = "-"
		b.infoTime = "-"
		return
	}
	path := b.files[b.idx]
	b.infoName = displayText(baseName(path))
	switch {
	case b.isParentEntry(path):
		b.infoKind = "parent directory"
	case b.isDirLike(path):
		b.infoKind = "directory"
	case b.isPDF(path):
		b.infoKind = fmt.Sprintf("PDF document, page %d", b.page)
	case isAnimatedWebp(path):
		b.infoKind = "animated webp"
	case b.isGraphic(path):
		b.infoKind = "image"
	case b.isArchive(path):
		b.infoKind = "archive"
	case b.isMp3(path):
		b.infoKind = "mp3 audio"
	default:
		if b.isArchiveMode() {
			b.infoKind = "archive entry"
		} else {
			b.infoKind = fileMime(path)
		}
	}
	if b.isArchiveMode() && !b.isParentEntry(path) && !b.isDirLike(path) {
		if size, ok := b.archiveEntrySize(path); ok {
			b.infoSize = humanSize(size)
		} else {
			b.infoSize = "-"
		}
		b.infoMode = "-"
		b.infoTime = "-"
		return
	}
	size := int64(0)
	if st, err := os.Stat(path); err == nil {
		size = st.Size()
	}
	b.infoSize = humanSize(size)
	mode := "-"
	if st, err := os.Stat(path); err == nil {
		mode = modeString(st.Mode())
	}
	b.infoMode = mode
	b.infoTime = "-"
	if st, err := os.Stat(path); err == nil {
		b.infoTime = st.ModTime().Format("2006-01-02 15:04")
	}
}

// archiveEntrySize reports the uncompressed size of an archive entry when the
// archive provides it (zip central directory).
func (b *Browser) archiveEntrySize(entry string) (int64, bool) {
	ctx := b.currentArchive()
	if ctx.isZip && ctx.zip != nil {
		for _, f := range ctx.zip.File {
			if f.Name == entry {
				return int64(f.UncompressedSize64), true
			}
		}
	}
	return 0, false
}

// fileType mirrors the shell's classification order: parent, symlink,
// directory, extension class, executable, default.
func (b *Browser) fileType(path string) string {
	if b.isParentEntry(path) {
		return "parent"
	}
	if isSymlink(path) {
		return "symlink"
	}
	if b.isDirLike(path) {
		return "directory"
	}
	ext := strings.ToLower(path)
	switch {
	case strings.HasSuffix(ext, ".pdf"):
		return "pdf"
	case hasSuffixAny(ext, ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".tga", ".webp", ".svg"):
		return "image"
	case hasSuffixAny(ext, ".zip", ".tar", ".tgz", ".gz", ".bz2", ".xz", ".7z", ".rar", ".cbz", ".cbr"):
		return "archive"
	case hasSuffixAny(ext, ".mp3", ".wav", ".flac", ".ogg", ".m4a", ".aac"):
		return "audio"
	case hasSuffixAny(ext, ".mp4", ".mkv", ".webm", ".avi", ".mov", ".mpeg", ".mpg"):
		return "video"
	case hasSuffixAny(ext, ".go", ".c", ".h", ".cc", ".cpp", ".cxx", ".rs", ".py", ".js", ".ts", ".tsx", ".jsx", ".java", ".sh", ".bash", ".zsh"):
		return "code"
	case hasSuffixAny(ext, ".json", ".yaml", ".yml", ".toml", ".ini", ".conf", ".xml"):
		return "config"
	case hasSuffixAny(ext, ".txt", ".md", ".markdown", ".html", ".htm", ".css", ".tex", ".bib", ".bbl", ".rst", ".log", ".csv"):
		return "text"
	default:
		if isExecutable(path) {
			return "executable"
		}
		return "default"
	}
}

func (b *Browser) isGraphic(path string) bool {
	return hasSuffixAny(strings.ToLower(path), ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".tga", ".webp", ".pdf")
}

func (b *Browser) isPDF(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".pdf")
}

// isArchive reports whether the entry is one of the supported browsable
// archives (zip/rar/cbz/cbr), which the browser treats as folders.
func (b *Browser) isArchive(path string) bool {
	return hasSuffixAny(strings.ToLower(path), ".zip", ".rar", ".cbz", ".cbr")
}

// isMp3 reports whether the entry is an mp3, which the browser itself decodes
// and opens instead of delegating to an external player.
func (b *Browser) isMp3(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".mp3")
}

// isAnimatedWebp reports whether the file is an animated WebP, by peeking at
// the RIFF/VP8X header. The browser performs no image work; the terminal plays
// it when the preview open DSL carries the `anim` option.
func isAnimatedWebp(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	hdr := make([]byte, 30)
	n, _ := io.ReadFull(f, hdr)
	return isAnimatedWebpHeader(hdr[:n])
}

// isAnimatedWebpHeader checks a header for an animated WebP container:
// RIFF....WEBPVP8X with the animation feature flag set.
func isAnimatedWebpHeader(b []byte) bool {
	if len(b) < 21 {
		return false
	}
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" || string(b[12:16]) != "VP8X" {
		return false
	}
	// VP8X chunk: 4-byte size then 1-byte feature flags (bit 1 = animation).
	return b[20]&0x02 != 0
}

func (b *Browser) isArchiveMode() bool { return len(b.archiveStack) > 0 }

func (b *Browser) currentArchive() *archiveCtx {
	return b.archiveStack[len(b.archiveStack)-1]
}

// isParentEntry reports whether entry is the ".." entry of the current view.
func (b *Browser) isParentEntry(entry string) bool {
	if b.isArchiveMode() {
		return entry == ".."
	}
	return entry == b.dir+"/.."
}

// isDirLike reports whether entry is a directory (real filesystem or archive).
func (b *Browser) isDirLike(entry string) bool {
	if b.isArchiveMode() {
		return strings.HasSuffix(entry, "/")
	}
	return isDir(entry)
}

func (b *Browser) entryLabel(path string) string {
	name := baseName(path)
	icon := b.icons[b.fileType(path)]
	return icon + "  " + displayText(name)
}

func (b *Browser) entryStyle(path string) string {
	if b.isDirLike(path) {
		return dirStyle
	}
	if b.isPDF(path) {
		return pdfStyle
	}
	if b.isGraphic(path) {
		return imageStyle
	}
	return resetStyle
}

// selectIndex mirrors select_index: redraw only what changed.
func (b *Browser) selectIndex(target int) {
	if len(b.files) == 0 {
		return
	}
	if target < 0 {
		target = 0
	}
	if target >= len(b.files) {
		target = len(b.files) - 1
	}
	if target == b.idx {
		return
	}
	old := b.idx
	oldTop := b.viewTop
	b.idx = target
	b.page = 1
	b.ensureVisible()
	b.updateInfo()
	b.renderPreview()
	if b.previewFullRedraw {
		b.drawOverlay()
	} else {
		b.drawInfo()
		if b.viewTop != oldTop {
			b.drawList()
		} else {
			b.drawListRow(old - b.viewTop)
			b.drawListRow(b.idx - b.viewTop)
		}
		b.drawStatus()
	}
	b.flush()
}

func (b *Browser) moveSelection(delta int) {
	if len(b.files) == 0 {
		return
	}
	b.selectIndex(b.idx + delta)
}

// changeDirectory resolves a target like `cd X && pwd -P`, then reloads.
func (b *Browser) changeDirectory(next, wanted string) {
	resolved, err := resolveDir(next)
	if err != nil {
		b.status = "Cannot enter directory"
		b.drawStatus()
		b.flush()
		return
	}
	b.dir = resolved
	b.dirLabel = resolved
	b.resetClick()
	b.idx = 0
	b.viewTop = 0
	b.page = 1
	b.buildList()
	if wanted != "" {
		for i, f := range b.files {
			if f == wanted {
				b.idx = i
				break
			}
		}
	}
	b.status = "Browsing " + displayText(b.dir)
	b.renderAll()
}

func (b *Browser) goParent() {
	if len(b.archiveStack) > 0 {
		ctx := b.currentArchive()
		if ctx.rel != "" {
			parent := ctx.rel
			if i := strings.LastIndex(parent, "/"); i >= 0 {
				parent = parent[:i]
			} else {
				parent = ""
			}
			ctx.rel = parent
			b.resetArchiveView()
			b.status = "Browsing " + displayText(b.archiveLabel())
			b.renderAll()
			return
		}
		b.closeArchive(ctx)
		b.archiveStack = b.archiveStack[:len(b.archiveStack)-1]
		if len(b.archiveStack) > 0 {
			b.resetArchiveView()
			b.status = "Browsing " + displayText(b.archiveLabel())
			b.renderAll()
		} else {
			// back to the real directory the archive was entered from.
			b.changeDirectory(b.archiveReturnDir, b.archiveReturnEntry)
		}
		return
	}
	if b.dir == "/" {
		return
	}
	old := b.dir
	parent := b.dir[:strings.LastIndex(b.dir, "/")]
	if parent == "" {
		parent = "/"
	}
	b.changeDirectory(parent, old)
}

// archiveLabel is the header path for the current archive location.
func (b *Browser) archiveLabel() string {
	ctx := b.currentArchive()
	if ctx.rel == "" {
		return ctx.file
	}
	return ctx.file + "/" + ctx.rel
}

// resetArchiveView re-lists the current archive directory after navigation.
func (b *Browser) resetArchiveView() {
	b.resetClick()
	b.idx = 0
	b.viewTop = 0
	b.page = 1
	b.buildList()
}

// enterArchive opens a zip/rar/cbz/cbr archive lazily: it reads only the entry
// listing and browses it virtually; file contents are extracted to /tmp on
// demand when opened or previewed.
func (b *Browser) enterArchive(archivePath string) {
	ctx := &archiveCtx{
		file:      archivePath,
		isZip:     hasSuffixAny(strings.ToLower(archivePath), ".zip", ".cbz"),
		extracted: map[string]string{},
	}
	if ctx.isZip {
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			b.status = "Cannot open archive " + baseName(archivePath)
			b.drawStatus()
			b.flush()
			return
		}
		ctx.zip = zr
		for _, f := range zr.File {
			ctx.names = append(ctx.names, strings.TrimSuffix(f.Name, "/"))
		}
	} else {
		names, err := listRarNames(archivePath)
		if err != nil {
			b.status = "Cannot open archive " + baseName(archivePath)
			b.drawStatus()
			b.flush()
			return
		}
		ctx.names = names
	}
	if len(b.archiveStack) == 0 {
		b.archiveReturnDir = b.dir
		b.archiveReturnEntry = archivePath
	}
	b.archiveStack = append(b.archiveStack, ctx)
	b.dirLabel = ctx.file
	b.resetArchiveView()
	b.status = "Browsing archive " + displayText(baseName(archivePath))
	b.renderAll()
}

// closeArchive releases an archive's open handles. Its lazily extracted files
// live under the cache dir and are removed with it on exit.
func (b *Browser) closeArchive(ctx *archiveCtx) {
	if ctx.zip != nil {
		ctx.zip.Close()
		ctx.zip = nil
	}
}

// enterArchiveDir descends into a virtual subdirectory of the open archive.
func (b *Browser) enterArchiveDir(entry string) {
	ctx := b.currentArchive()
	name := strings.TrimSuffix(entry, "/")
	if ctx.rel == "" {
		ctx.rel = name
	} else {
		ctx.rel = ctx.rel + "/" + name
	}
	b.dirLabel = b.archiveLabel()
	b.resetArchiveView()
	b.status = "Browsing " + displayText(b.archiveLabel())
	b.renderAll()
}

// materializeEntry extracts a single archive entry to /tmp (lazily, cached).
func (b *Browser) materializeEntry(ctx *archiveCtx, entry string) (string, error) {
	if p, ok := ctx.extracted[entry]; ok {
		return p, nil
	}
	if ctx.tmpDir == "" {
		d, err := os.MkdirTemp(b.cacheDir, "arc-")
		if err != nil {
			return "", err
		}
		ctx.tmpDir = d
	}
	var (
		realPath string
		err      error
	)
	if ctx.isZip {
		realPath, err = extractZipEntry(ctx.zip, entry, ctx.tmpDir)
	} else {
		realPath, err = extractRarEntry(ctx.file, entry, ctx.tmpDir)
	}
	if err != nil {
		return "", err
	}
	ctx.extracted[entry] = realPath
	return realPath, nil
}

func (b *Browser) enterSelected() {
	if b.idx < 0 {
		return
	}
	path := b.files[b.idx]
	switch {
	case b.isParentEntry(path):
		b.goParent()
	case b.isDirLike(path):
		if b.isArchiveMode() {
			b.enterArchiveDir(path)
		} else {
			b.changeDirectory(path, "")
		}
	case b.isArchive(path):
		if b.isArchiveMode() {
			real, err := b.materializeEntry(b.currentArchive(), path)
			if err != nil {
				b.status = "Cannot extract " + baseName(path)
				b.drawStatus()
				b.flush()
				return
			}
			path = real
		}
		b.enterArchive(path)
	default:
		b.openSelected(0)
	}
}

func (b *Browser) openPath(path string, minimize int) {
	name := baseName(path)
	if b.isArchiveMode() && !b.isParentEntry(path) {
		real, err := b.materializeEntry(b.currentArchive(), path)
		if err != nil {
			b.status = "Cannot extract " + name
			b.drawStatus()
			b.flush()
			return
		}
		path = real
	}
	if b.openCmd == "" {
		b.status = "External opening is disabled"
	} else {
		if minimize != 0 {
			b.windowDCS(fmt.Sprintf("place bottom-left 0px 0px 24%% 8%% restore %s", b.geometryTag))
		}
		cmd := exec.Command("bash", "-c", b.openCmd)
		cmd.Env = append(os.Environ(), "FILE="+path, "NAME="+name, "BROWSER_DIR="+b.dir)
		// nil Stdin/Stdout/Stderr map to /dev/null, matching the shell redirect.
		_ = cmd.Start()
		b.status = "Opened " + displayText(name)
	}
	b.drawStatus()
	b.flush()
}

func (b *Browser) openSelected(minimize int) {
	if b.idx < 0 {
		return
	}
	path := b.files[b.idx]
	if b.isDirLike(path) || b.isArchive(path) {
		b.enterSelected()
		return
	}
	if b.isMp3(path) {
		b.openMp3(path)
		return
	}
	b.openPath(path, minimize)
}

func (b *Browser) doubleClickSelected() {
	if b.idx < 0 {
		return
	}
	if b.isDirLike(b.files[b.idx]) || b.isArchive(b.files[b.idx]) {
		b.enterSelected()
	} else {
		b.openSelected(1)
	}
}
func (b *Browser) refreshList() {
	selected := ""
	b.resetClick()
	if b.idx >= 0 {
		selected = b.files[b.idx]
	}
	b.buildList()
	b.idx = 0
	for i, f := range b.files {
		if f == selected {
			b.idx = i
			break
		}
	}
	b.status = "Refreshed"
	b.renderAll()
}

func (b *Browser) toggleHidden() {
	if b.showHidden {
		b.showHidden = false
		b.status = "Hidden files off"
	} else {
		b.showHidden = true
		b.status = "Hidden files on"
	}
	b.refreshList()
}

func (b *Browser) changePDFPage(delta int) {
	if b.idx < 0 {
		return
	}
	if !b.isPDF(b.files[b.idx]) {
		return
	}
	b.page += delta
	if b.page < 1 {
		b.page = 1
	}
	b.updateInfo()
	b.renderPreview()
	b.drawInfo()
	b.drawStatus()
	b.flush()
}

func (b *Browser) resetClick() {
	b.lastClickIdx = -1
	b.lastClickMs = 0
}

// runShellCommand runs a : prompt command in the foreground after leaving the
// alternate screen, then restores the UI (mirrors ranger-style shell escapes).
func (b *Browser) runShellCommand(code string) {
	if strings.TrimSpace(code) == "" {
		b.status = "Nothing to run"
		b.renderAll()
		return
	}
	f := ""
	name := ""
	if b.idx >= 0 && b.idx < len(b.files) {
		f = b.files[b.idx]
		name = baseName(f)
		if b.isArchiveMode() && !b.isParentEntry(f) && !b.isDirLike(f) {
			// $F for a command inside an archive must be the extracted copy.
			if real, err := b.materializeEntry(b.currentArchive(), f); err == nil {
				f = real
			}
		}
	}
	fmt.Fprint(b.out, "\x1b[?1006l\x1b[?1000l\x1b[?25h\x1b[0m\x1b[?1049l")
	b.flush()
	cmd := exec.Command("bash", "-c", code)
	cmd.Dir = b.dir
	cmd.Env = append(os.Environ(), "D="+b.dir, "F="+f, "NAME="+name, "BROWSER_DIR="+b.dir)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
	b.makeRaw()
	fmt.Fprint(b.out, "\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h")
	b.status = "Command finished"
	b.renderAll()
}

func (b *Browser) renderAll() {
	b.paintStop()
	b.terminalSize()
	if b.compact {
		fmt.Fprintf(b.out, "\x1b[2J\x1b[H%s click to restore %s", headerStyle, resetStyle)
		b.paintResume()
		b.flush()
		return
	}
	b.ensureVisible()
	b.updateInfo()
	fmt.Fprint(b.out, "\x1b[2J")
	b.atlasClean = true
	b.previewWasGraphic = false
	b.renderPreview()
	b.drawOverlay()
	b.paintResume()
	b.flush()
}

func (b *Browser) renderPreview() {
	b.previewFullRedraw = false
	if b.idx < 0 || b.idx >= len(b.files) {
		b.clearPreview()
		b.drawPreviewMessage("EMPTY DIRECTORY", "No entries to preview.", "")
		return
	}
	path := b.files[b.idx]
	if b.isArchiveMode() {
		if b.isParentEntry(path) || b.isDirLike(path) {
			b.previewWasGraphic = false
			b.clearPreview()
			title := displayText(baseName(path))
			if b.isParentEntry(path) {
				title = ".."
			}
			b.drawPreviewMessage("DIRECTORY", title, "Double-click or press Enter to browse.")
			return
		}
		real, err := b.materializeEntry(b.currentArchive(), path)
		if err != nil {
			b.previewWasGraphic = false
			b.clearPreview()
			b.drawPreviewMessage("CANNOT EXTRACT", displayText(baseName(path)), "The archive entry could not be extracted.")
			return
		}
		path = real
	}
	if b.isGraphic(path) && isRegularFile(path) {
		b.clearPreview()
		b.setPreviewLink(path)
		title := "PREVIEW  "
		anim := ""
		if isAnimatedWebp(path) {
			title = "ANIMATED  "
			anim = " anim"
		}
		b.drawPreviewTitle(title + displayText(baseName(path)))
		h := b.rows - 3
		if h < 1 {
			h = 1
		}
		if b.isPDF(path) {
			b.dcs(fmt.Sprintf("open '%s' rect 1 3 %d %d fit-contain page %d", b.previewLink, b.previewW, h, b.page))
		} else {
			b.dcs(fmt.Sprintf("open '%s' rect 1 3 %d %d fit-contain%s", b.previewLink, b.previewW, h, anim))
		}
		b.atlasClean = false
		b.previewWasGraphic = true
		return
	}
	b.previewWasGraphic = false
	b.clearPreview()
	title := displayText(baseName(path))
	if b.isArchive(path) {
		b.drawPreviewMessage("ARCHIVE", title, "Enter or double-click to browse the contents.")
	} else if isDir(path) {
		b.drawPreviewMessage("DIRECTORY", title, "Double-click or press Enter to browse.")
	} else if !isReadable(path) {
		b.drawPreviewMessage("UNREADABLE", title, "Read permission is required for a preview.")
	} else if b.isMp3(path) {
		b.drawMp3Preview(path)
	} else if b.isTextFile(path) {
		h := b.rows - 3
		if h < 1 {
			h = 1
		}
		w := b.previewW - 3
		if w < 1 {
			w = 1
		}
		b.setPreviewLink(path)
		b.drawPreviewTitle("TEXT  " + title)
		b.dcs(fmt.Sprintf("open '%s' rect 2 3 %d %d", b.previewLink, w, h))
	} else {
		b.drawPreviewMessage("BINARY FILE", title, "No inline preview is available for this type.")
	}
}

// setPreviewLink points the safe temp symlink at path (like `ln -sfn`).
func (b *Browser) setPreviewLink(path string) {
	os.Remove(b.previewLink)
	_ = os.Symlink(path, b.previewLink)
}

// drawMp3Preview renders the decoded mp3 metadata card in the preview pane.
// Decoding is cached per path so the full stream is only scanned once.
func (b *Browser) drawMp3Preview(path string) {
	b.previewWasGraphic = false
	b.clearPreview()
	name := displayText(baseName(path))
	meta, ok := b.mp3Cache[path]
	if !ok {
		m, err := readMp3Meta(path)
		if err != nil {
			b.drawPreviewMessage("MP3 AUDIO", name, "Could not decode.")
			return
		}
		meta = m
		b.mp3Cache[path] = meta
	}
	title := meta.title
	if title == "" {
		title = name
	}
	b.drawPreviewTitle("MP3 AUDIO  " + shorten(title, b.previewW-14))
	row := 4
	if meta.title != "" {
		b.drawText(row, 3, b.previewW-4, resetStyle, "title   "+meta.title)
		row++
	}
	if meta.artist != "" {
		b.drawText(row, 3, b.previewW-4, resetStyle, "artist  "+meta.artist)
		row++
	}
	if meta.album != "" {
		b.drawText(row, 3, b.previewW-4, resetStyle, "album   "+meta.album)
		row++
	}
	b.drawText(row, 3, b.previewW-4, resetStyle, "time    "+meta.durationString())
	row++
	if meta.sampleRate > 0 {
		b.drawText(row, 3, b.previewW-4, resetStyle, fmt.Sprintf("rate    %d Hz", meta.sampleRate))
		row++
	}
	if meta.bitrateKbps > 0 {
		b.drawText(row, 3, b.previewW-4, dimStyle, fmt.Sprintf("bitrate %d kbps", meta.bitrateKbps))
	}
}

// drawMp3View renders the in-browser mp3 viewer (the open state).
func (b *Browser) drawMp3View(path string) {
	b.paintStop()
	b.drawMp3Preview(path)
	switch {
	case b.playback != nil:
		b.status = "Playing  q stop"
	case b.playbackErr != nil:
		b.status = "No audio  q close"
	default:
		b.status = "MP3 viewer  q close"
	}
	b.drawStatus()
	b.paintResume()
	b.flush()
}

// openMp3 opens an mp3 in the browser itself: it decodes the file, shows a
// viewer modal with its metadata, and streams the decoded PCM to the static
// ALSA device. If no sound device is available it degrades to a metadata-only
// viewer.
func (b *Browser) openMp3(path string) {
	if b.isArchiveMode() {
		real, err := b.materializeEntry(b.currentArchive(), path)
		if err != nil {
			b.status = "Cannot extract " + baseName(path)
			b.drawStatus()
			b.flush()
			return
		}
		path = real
	}
	meta, ok := b.mp3Cache[path]
	if !ok {
		if m, err := readMp3Meta(path); err == nil {
			meta = m
			b.mp3Cache[path] = meta
		}
	}
	b.mp3Active = true
	b.playbackErr = nil
	b.playbackStop = nil
	if player, err := openALSA(meta.sampleRate); err != nil {
		b.playbackErr = err
		b.playback = nil
	} else {
		b.playback = player
		b.playbackStop = make(chan struct{})
		go b.streamMp3(path, player)
	}
	b.drawMp3View(path)
	for b.mp3Active {
		b.processPendingSignals()
		if !b.running {
			b.mp3Active = false
			break
		}
		if b.resized {
			b.resized = false
			b.renderAll()
			if b.compact {
				b.mp3Active = false
				break
			}
			b.drawMp3View(path)
		}
		key, st := b.in.readByte(100 * time.Millisecond)
		switch st {
		case inTimeout:
			continue
		case inEOF:
			b.mp3Active = false
			b.running = false
			break
		case inData:
			switch key {
			case 'q', 'Q', 0x03, 0x1b:
				b.mp3Active = false
			}
		}
	}
	b.stopPlayback()
	b.status = "Closed mp3"
	b.renderAll()
}

// loop is the main input loop.
func (b *Browser) loop() {
	b.running = true
	for b.running {
		b.processPendingSignals()
		if !b.running {
			break
		}
		if b.resized {
			b.resized = false
			b.status = "Resized"
			b.renderAll()
		}
		key, st := b.in.readByte(100 * time.Millisecond)
		switch st {
		case inData:
			b.handleKey(key)
		case inEOF:
			b.running = false
		case inTimeout:
			// re-check resize at the top of the loop
		}
	}
}

// handleSignal processes pending signals in the loop's goroutine so prompt
// state is never accessed from the signal handler.
func (b *Browser) processPendingSignals() {
	for {
		select {
		case sig := <-b.sigCh:
			switch sig {
			case syscall.SIGWINCH:
				b.resized = true
			case syscall.SIGINT:
				if b.promptActive {
					b.promptAbort = true
				} else if b.manualActive {
					b.manualActive = false
				} else if b.mp3Active {
					b.mp3Active = false
				} else {
					b.exitCode = 130
					b.running = false
				}
			case syscall.SIGTERM, syscall.SIGHUP:
				b.exitCode = 130
				b.running = false
			}
		default:
			return
		}
	}
}

// helpers

func hasSuffixAny(s string, suffixes ...string) bool {
	for _, sf := range suffixes {
		if strings.HasSuffix(s, sf) {
			return true
		}
	}
	return false
}

func baseName(path string) string {
	p := strings.TrimSuffix(path, "/")
	if p == "" {
		return "/"
	}
	return filepath.Base(p)
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isSymlink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}

func isRegularFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isReadable(p string) bool {
	return unix.Access(p, unix.R_OK) == nil
}

func isExecutable(p string) bool {
	return unix.Access(p, unix.X_OK) == nil
}

func resolveDir(next string) (string, error) {
	abs, err := filepath.Abs(next)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(resolved)
	if err != nil || !st.IsDir() {
		return "", os.ErrInvalid
	}
	return resolved, nil
}

func humanSize(n int64) string {
	unit := "B"
	if n >= 1<<30 {
		n /= 1 << 30
		unit = "GiB"
	} else if n >= 1<<20 {
		n /= 1 << 20
		unit = "MiB"
	} else if n >= 1024 {
		n /= 1024
		unit = "KiB"
	}
	return fmt.Sprintf("%d %s", n, unit)
}

// modeString mirrors `stat -c %A` (ls-style symbolic mode).
func modeString(m os.FileMode) string {
	out := []byte("----------")
	switch {
	case m.IsDir():
		out[0] = 'd'
	case m&os.ModeSymlink != 0:
		out[0] = 'l'
	case m&os.ModeNamedPipe != 0:
		out[0] = 'p'
	case m&os.ModeSocket != 0:
		out[0] = 's'
	case m&os.ModeDevice != 0:
		if m&os.ModeCharDevice != 0 {
			out[0] = 'c'
		} else {
			out[0] = 'b'
		}
	}
	perm := m.Perm()
	bits := [3]struct{ r, w, x os.FileMode }{
		{0400, 0200, 0100},
		{0040, 0020, 0010},
		{0004, 0002, 0001},
	}
	for i, g := range bits {
		if perm&g.r != 0 {
			out[1+i*3] = 'r'
		}
		if perm&g.w != 0 {
			out[2+i*3] = 'w'
		}
		if perm&g.x != 0 {
			out[3+i*3] = 'x'
		}
	}
	return string(out)
}

// fileMime returns the `file -Lb --mime-type` result when available.
func fileMime(path string) string {
	if !fileCommandAvailable {
		return "file"
	}
	out, err := exec.Command("file", "-Lb", "--mime-type", "--", path).Output()
	if err != nil {
		return "file"
	}
	mime := strings.TrimSpace(string(out))
	if mime == "" {
		return "file"
	}
	return mime
}

var fileCommandAvailable = func() bool {
	_, err := exec.LookPath("file")
	return err == nil
}()

// isTextFile mirrors the shell: `file` when present, else a binary sniff.
func (b *Browser) isTextFile(path string) bool {
	if fileCommandAvailable {
		mime := fileMime(path)
		if strings.HasPrefix(mime, "text/") {
			return true
		}
		switch mime {
		case "application/json", "application/xml", "application/x-shellscript", "application/javascript":
			return true
		}
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := io.ReadFull(f, buf)
	data := buf[:n]
	if len(data) == 0 {
		return false
	}
	for _, c := range data {
		if c == 0 {
			return false
		}
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' && c != '\f' && c != '\b' {
			return false
		}
	}
	return true
}

func nowMS() int64 {
	return time.Now().UnixMilli()
}
