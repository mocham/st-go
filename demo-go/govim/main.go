// govim is a native Go modal terminal editor with a write-ahead recovery journal.
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type commandsFlag []string

func (c *commandsFlag) String() string         { return strings.Join(*c, "; ") }
func (c *commandsFlag) Set(value string) error { *c = append(*c, value); return nil }

func run(args []string) (code int) {
	options := defaultJournalOptions()
	previewOptions := defaultPreviewOptions()
	flags := flag.NewFlagSet("govim", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	recoverFile := flags.Bool("r", false, "recover a govim swap for FILE (without FILE, list swap files)")
	flags.BoolVar(recoverFile, "recover", false, "recover a govim swap")
	readonly := flags.Bool("R", false, "open readonly (explicit :w! can write)")
	noPreview := flags.Bool("no-preview", false, "disable automatic LaTeX/Markdown preview")
	flags.IntVar(&previewOptions.width, "preview-width", previewOptions.width, "preview width percent (editor uses the remainder)")
	flags.StringVar(&previewOptions.background, "preview-bg", previewOptions.background, "preview paper/canvas color (#RRGGBB)")
	flags.DurationVar(&previewOptions.gap, "preview-gap", previewOptions.gap, "minimum background preview render gap (rapid saves coalesce)")
	flags.StringVar(&options.Directory, "swap-dir", "", "persistent swap directory (default: beside file, falling back to user cache)")
	flags.DurationVar(&options.Interval, "sync-interval", options.Interval, "fsync changed journals at this interval, even during continuous typing")
	flags.IntVar(&options.Bytes, "sync-bytes", options.Bytes, "fsync after this many additional journal bytes")
	var commands commandsFlag
	flags.Var(&commands, "c", "execute an Ex command on startup (repeatable)")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: govim [options] [+LINE] [FILE ...]\n\nNative Go modal editor. Fixed preferences from TEST-ASSETS/vimrc0; no Vimscript.\nAll text changes are journaled before applying; :preserve forces fsync.\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if options.Interval < time.Millisecond || options.Interval > time.Minute || options.Bytes < 1 || options.Bytes > maxBufferBytes {
		fmt.Fprintln(os.Stderr, "govim: invalid sync interval/threshold (1ms..1m; 1..16 MiB)")
		return 2
	}
	_, colorErr := strconv.ParseUint(strings.TrimPrefix(previewOptions.background, "#"), 16, 24)
	if previewOptions.width < 1 || previewOptions.width > 99 || previewOptions.gap < 0 || previewOptions.gap > time.Minute || len(previewOptions.background) != 7 || previewOptions.background[0] != '#' || colorErr != nil {
		fmt.Fprintln(os.Stderr, "govim: invalid preview options (width 1..99; gap 0..1m; background #RRGGBB)")
		return 2
	}
	if options.Directory != "" {
		var err error
		options.Directory, err = filepath.Abs(options.Directory)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	paths := flags.Args()
	line := 0
	if len(paths) > 0 && strings.HasPrefix(paths[0], "+") {
		var err error
		line, err = strconv.Atoi(strings.TrimPrefix(paths[0], "+"))
		if err != nil || line < 1 {
			fmt.Fprintln(os.Stderr, "govim: +LINE must be positive")
			return 2
		}
		paths = paths[1:]
	}
	if *recoverFile && len(paths) == 0 {
		return listRecovery(options)
	}
	e := newEditor(options)
	e.arguments = append([]string(nil), paths...)
	var restore func()
	defer func() {
		problem := recover()
		normalExit := problem == nil && code == 0 && e.quit
		if err := e.close(normalExit); err != nil {
			fmt.Fprintln(os.Stderr, "govim: preserve/close:", err)
			if code == 0 {
				code = 1
			}
		}
		if restore != nil {
			restore()
		}
		if problem != nil {
			fmt.Fprintln(os.Stderr, "govim: internal error; swap journals retained for recovery:", problem)
			code = 1
		}
	}()
	path := ""
	if len(paths) > 0 {
		path = paths[0]
	}
	var b *Buffer
	var err error
	if *recoverFile && strings.HasSuffix(path, ".govim.swp") {
		meta, err := swapMetadata(path, options)
		if err != nil {
			fmt.Fprintln(os.Stderr, "govim:", err)
			return 1
		}
		b = &Buffer{meta: meta}
		if err := recoverJournal(b, path, options); err != nil {
			fmt.Fprintln(os.Stderr, "govim:", err)
			return 1
		}
		w := &Window{buffer: b, cursor: b.normal(b.recoveryCursor), goal: -1, lineOriginalRow: -1}
		e.buffers = []*Buffer{b}
		e.tabs = []*tab{{root: &pane{window: w}, active: w}}
		e.message = b.warning
	} else {
		b, err = e.open(path, *recoverFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "govim:", err)
			return 1
		}
	}
	b.readonly = *readonly
	if line > 0 {
		e.setPos(firstNonblank(b, clamp(line-1, 0, len(b.lineStarts())-1)))
	}
	for _, command := range commands {
		e.execute(command)
		if e.quit {
			return 0
		}
	}
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "govim: stdin must be a terminal:", err)
		return 1
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	restore = func() {
		fmt.Fprint(os.Stdout, "\x18\x1b[?2026l\x1b[?2004l\x1b[?1002l\x1b[?1000l\x1b[?1006l\x1b[?7h\x1b[0 q\x1b[0m\x1b[?25h\x1b[?1049l")
		unix.IoctlSetTermios(fd, unix.TCSETS, old)
	}
	clip := &terminalClipboard{out: os.Stdout}
	e.clip = clip
	signals := make(chan os.Signal, 16)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	runner := &externalRunner{editor: e, fd: fd, old: old, raw: &raw, signals: signals}
	e.runCommand = runner.shell
	e.compile = runner.tex
	if !*noPreview {
		e.preview = newEditorPreview(previewOptions, os.Stdout)
	}
	fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?7l\x1b[?2004h\x1b[?1000h\x1b[?1002h\x1b[?1006h")
	resize := func() {
		if size, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err == nil {
			e.rows, e.cols = clamp(int(size.Row), 2, 500), clamp(int(size.Col), 8, 1000)
		}
		e.dirty = true
	}
	resize()
	var parser inputParser
	var renderer renderer
	var input [65536]byte
	for !e.quit {
		for draining := true; draining; {
			select {
			case sig := <-signals:
				switch sig {
				case syscall.SIGWINCH:
					resize()
				case syscall.SIGINT:
					e.Feed(keyEvent{Key: "\x03"})
				case syscall.SIGTERM, syscall.SIGHUP:
					e.message = "Interrupted; journals retained"
					return 128 + int(sig.(syscall.Signal))
				}
			default:
				draining = false
			}
		}
		now := time.Now()
		parser.expire(now, e.Feed)
		e.tick(now)
		if err := e.preview.tick(e, now); err != nil {
			return 1
		}
		if err := clip.Flush(); err != nil {
			return 1
		}
		if e.dirty {
			if err := e.draw(&renderer, os.Stdout); err != nil {
				return 1
			}
		}
		timeout := min(50, max(1, int(options.Interval/time.Millisecond)))
		// A tiny completed syntax slice should not wait a full idle interval
		// before committing an edit's single colored frame.
		for _, buffer := range e.buffers {
			if buffer.syntax.waiting {
				timeout = min(timeout, 5)
				break
			}
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(poll, timeout)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 1
		}
		if poll[0].Revents&unix.POLLIN != 0 {
			n, err := unix.Read(fd, input[:])
			if err != nil && err != unix.EINTR {
				return 1
			}
			if n == 0 && err == nil {
				return 1
			}
			if n > 0 {
				parser.feed(input[:n], time.Now(), e.Feed)
			}
		} else if poll[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return 1
		}
	}
	return 0
}
func main() { os.Exit(run(os.Args[1:])) }

func swapMetadata(path string, options journalOptions) (fileMetadata, error) {
	j, err := openJournal(path, false, options)
	if err != nil {
		return fileMetadata{}, err
	}
	defer j.file.Close()
	magic := make([]byte, len(journalMagic))
	if _, err := io.ReadFull(j.file, magic); err != nil || string(magic) != journalMagic {
		return fileMetadata{}, fmt.Errorf("not a govim swap")
	}
	var header [8]byte
	if _, err := io.ReadFull(j.file, header[:]); err != nil {
		return fileMetadata{}, err
	}
	length := int(binary.LittleEndian.Uint32(header[:4]))
	if length < 1 || length > maxRecordBytes {
		return fileMetadata{}, fmt.Errorf("invalid snapshot length")
	}
	info, err := j.file.Stat()
	if err != nil {
		return fileMetadata{}, err
	}
	if int64(length) > info.Size()-int64(len(journalMagic))-8 {
		return fileMetadata{}, fmt.Errorf("incomplete snapshot")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(j.file, data); err != nil {
		return fileMetadata{}, err
	}
	if crc32.Checksum(data, journalCRC) != binary.LittleEndian.Uint32(header[4:]) {
		return fileMetadata{}, fmt.Errorf("snapshot checksum mismatch")
	}
	var record journalRecord
	if json.Unmarshal(data, &record) != nil || record.Kind != "snapshot" || record.Meta == nil {
		return fileMetadata{}, fmt.Errorf("invalid snapshot")
	}
	return *record.Meta, nil
}
func listRecovery(options journalOptions) int {
	cache, err := os.UserCacheDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	directory := options.Directory
	if directory == "" {
		directory = filepath.Join(cache, "govim", "swap")
	}
	patterns := []string{filepath.Join(directory, "*.govim.swp"), ".*.govim.swp"}
	found := false
	for _, pattern := range patterns {
		files, _ := filepath.Glob(pattern)
		for _, file := range files {
			meta, err := swapMetadata(file, options)
			if err != nil {
				fmt.Printf("%q: %s\n", file, err)
				continue
			}
			fmt.Printf("%q  ->  %q\n", file, meta.Path)
			found = true
		}
	}
	if !found {
		fmt.Println("No recoverable govim swaps in the current directory or swap cache.")
	}
	return 0
}

const helpText = `govim — native modal editor

NORMAL: h j k l/arrows; counts; w W b B e E ge gE; 0 ^ $ g_ |;
gg G H M L; f F t T ; ,; % ( ) { }; [[ ]]; marks m / ' / backtick;
Ctrl-F/B/D/U/E/Y; zt zz zb; Ctrl-O/I; g; g,; gf; Ctrl-^.

EDIT: i I a A o O; R r; d c y + any motion; dd cc yy;
x X s S D C Y; p P; J gJ; > < =; g~ gu gU gq; ~; Ctrl-A/X.
Text objects: iw aw iW aW is as ip ap; quotes; (), [], {}, <>.
VISUAL: v V Ctrl-V; motions, o, y d c r ~ u U > < = J p;
block I/A and block registers. gv reselects the previous selection.
INSERT: Escape; arrows; Backspace/Delete; auto/smart indent;
Tab=4 spaces, Ctrl-W/U/T/D/R/V/O/N/P; bracketed paste.
UNDO: u / Ctrl-R, U; dot repeats a change; q{reg} records,
@{reg} plays and @@ repeats a macro. "{reg} chooses a register.
yy/dd and Visual y/d also request a host clipboard copy using OSC 52.
Internal registers/p/P are immediate and separate from host clipboard reads.
Explicit "+/"* reads are asynchronous; Escape cancels a pending read.

SEARCH: / ? n N * #, Vim basic magic, \v (RE2), \V (literal).
:noh clears highlights. :[range]s/pattern/replacement/gicn
supports &, \1..\9 and \r; c asks y/n/a/q/l.

PROMPTS (: / ?): Left/Right or Ctrl-B/F; Home/End or Ctrl-A/E;
Backspace/Delete/Ctrl-D; Ctrl-U/K/W kill, Ctrl-Y yank;
Ctrl-Left/Right or Alt-B/F word movement; Up/Down or Ctrl-P/N history.
Type and paste at the cursor; long prompts scroll horizontally.

EX: :w :w! :q :q! :wq :x :qa :wqa :wa :e :enew :n :prev
:args :ls :b :bn :bp :bd :saveas :read :file :cd :pwd
:split :vsplit :new :vnew :close :only :tabnew :tabnext :tabclose
Ctrl-W s/v/w/h/j/k/l/c/o/= and resize commands navigate windows.
:delete :yank :Y :D :put :join :copy :move :sort :retab
:normal :g/pattern/d :v/pattern/d :undo :redo :marks :registers
:jumps :changes :preserve :swapname :recover FILE :! COMMAND
:Gn[!] [FILE] and Ctrl-F5 run pdflatex/bibtex, honoring %main=.
TEX INSERT: embedded JSON IMAPs expand backtick+a to \alpha, etc.
F5 expands a standalone environment name (or a package in the preamble).
Ctrl-J consumes the next <++> stop. Enter remains a newline.
CJK INSERT: [[[ starts Pinyin; +/- pages; 1..5 chooses; ]]] ends.
Normal ]]] removes both triples; Escape/failure keeps them for recovery.
Candidates appear in the command row. A helper is spawned only on demand.
govim-pinyind isolates native faults; typed spelling survives failures.

PREVIEW: LaTeX/Markdown opens st-go's retained right panel automatically.
Save to refresh; %cursor wins, otherwise follow the cursor saved with :w.
:wnomove[!] refreshes without jumping. Wheel scrolls the preview locally.
:Preview [LINE] / :PreviewScroll COUNT navigate manually. --no-preview disables.
:toggle seeks the disk preview to the cursor; :toggle! draws unsaved buffers
and seeks there instead. Neither writes files; both override %cursor.
Normal Shift-Tab toggles the same panel between preview and source TOC.
Wheel scrolls smoothly; click a heading/label to navigate the editor.
Tab/Ctrl-I still moves forward through jump history.
The bottom row shows the last successful save's date/time, not a second path.

RECOVERY: every text change (including undo/redo) is written to a
checksummed .govim.swp before it is applied. fsync defaults to
1 second / 64 KiB. :preserve forces it. govim -r FILE recovers.
An existing swap is never overwritten. :q! deliberately discards.

Fixed vimrc0 preferences, pure-Go Chroma syntax/desert colors; no Vimscript,
plugins, folds, tags engine, LSP, arbitrary options, or Vim swap ABI.
Buffers are bounded to 16 MiB; RE2 searches to 65536 matches.
Use :help for this reference. Press any key to return.`
