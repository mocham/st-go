package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"st-go/limits"
	"st-go/pdfmeta"
	"st-go/tui/navigation"
)

type viewer struct {
	options
	out                                        io.Writer
	err                                        error
	cols                                       int
	pages, position                            int
	loading, failed, quit                      bool
	prompt, count, prefix, message, lastStatus string
	marks                                      map[string]int
	previous                                   int
	search                                     string
	searchBackward                             bool
	rows                                       int
	panel                                      panelMode
	panelDirty                                 bool
	panelError                                 string
	toc                                        []pdfmeta.Entry
	tocLoaded                                  bool
	tocSelection                               navigation.Selection
	pageText                                   string
	textPage, textTop                          int
	textLines                                  []string
	metadataToken                              int
	pendingMetadata                            *metadataRequest
}

func quoteDSL(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func (v *viewer) write(s string) {
	if v.err == nil {
		_, v.err = io.WriteString(v.out, s)
	}
}

func (v *viewer) command(s string) { v.write("\x1bPregion secondary " + s + "\x1b\\") }

func (v *viewer) open(path string) {
	v.file = path
	v.command("open " + quoteDSL(path) + " background " + quoteDSL(v.background) + " fit-width continuous")
	v.loading, v.failed = true, false
	v.toc, v.tocLoaded, v.pageText, v.textPage = nil, false, "", 0
	v.pendingMetadata, v.panelError, v.panelDirty = nil, "", true
	// Zoom is kept by the terminal across reloads and file changes.
}

func (v *viewer) navigate(kind string, n int) {
	v.command(fmt.Sprintf("%s %d", kind, n))
}

func (v *viewer) jump(kind string, n int) {
	v.previous = v.position
	v.navigate(kind, n)
}

func (v *viewer) zoomTo(n int) {
	if n != 0 {
		n = max(10, min(n, 500))
		v.zoom = n
	}
	v.navigate("zoom", n)
}

func (v *viewer) key(key string) {
	if v.prompt != "" {
		switch key {
		case "escape", "\x03":
			v.prompt = ""
		case "\r", "\n":
			command, mode := strings.TrimSpace(v.prompt[1:]), v.prompt[0]
			v.prompt = ""
			if mode == ':' {
				v.execute(command)
			} else if command != "" {
				v.search, v.searchBackward = command, mode == '?'
				v.find(false, false)
			}
		case "\x7f", "\b":
			_, n := utf8.DecodeLastRuneInString(v.prompt)
			v.prompt = v.prompt[:len(v.prompt)-n]
		case "\x15":
			v.prompt = v.prompt[:1]
		default:
			if len(v.prompt)+len(key) <= 4096 && len([]rune(key)) == 1 && !unicode.IsControl([]rune(key)[0]) {
				v.prompt += key
			}
		}
		return
	}
	if key == "escape" || key == "\x03" {
		v.prefix, v.count, v.message = "", "", ""
		if v.panel != panelNone {
			v.showPanel(panelNone)
		}
		return
	}
	if key == "\t" {
		v.togglePanel(panelTOC)
		return
	}
	if key == "backtab" {
		v.togglePanel(panelText)
		return
	}
	if v.panelKey(key) {
		v.count, v.prefix = "", ""
		return
	}
	v.message = ""
	if v.prefix == "m" || v.prefix == "'" {
		if len(key) == 1 {
			if v.prefix == "m" {
				v.marks[key] = v.position
				v.message = "Mark " + key + " set"
			} else if key == "'" {
				old := v.previous
				v.jump("position", old)
			} else if position, ok := v.marks[key]; ok {
				v.jump("position", position)
			} else {
				v.message = "No mark " + key
			}
		}
		v.prefix, v.count = "", ""
		return
	}
	if v.prefix == "z" && key == "0" {
		v.zoomTo(100)
		v.prefix, v.count = "", ""
		return
	}
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		if len(v.count) < 6 {
			v.count += key
		}
		return
	}
	n, _ := strconv.Atoi(v.count)
	repeat := max(1, n)
	prefix := v.prefix
	v.prefix = ""
	switch key {
	case "q":
		v.quit = true
	case "\x04":
		v.navigate("view", 500000*min(repeat, limits.MaxBytes/1000000))
	case "j", "down":
		v.navigate("scroll", repeat)
	case "k", "up":
		v.navigate("scroll", -repeat)
	case "h", "left":
		v.navigate("pan", -repeat)
	case "l", "right":
		v.navigate("pan", repeat)
	case "J":
		v.jump("pages", repeat)
	case "K":
		v.jump("pages", -repeat)
	case " ", "\x06", "pagedown":
		v.navigate("view", 1000000*min(repeat, limits.MaxBytes/1000000))
	case "\x02", "pageup":
		v.navigate("view", -1000000*min(repeat, limits.MaxBytes/1000000))
	case "\x15":
		v.navigate("view", -500000*min(repeat, limits.MaxBytes/1000000))
	case "g":
		if prefix == "g" {
			v.jump("page", repeat)
		} else {
			v.prefix = "g"
			return
		}
	case "home":
		v.jump("position", 0)
	case "G", "end":
		if n > 0 {
			v.jump("page", n)
		} else {
			v.jump("position", 1000000)
		}
	case "%":
		v.jump("position", min(100, n)*10000)
	case "+", "=":
		v.zoomTo(v.zoom + 10*min(repeat, 50))
	case "-":
		v.zoomTo(v.zoom - 10*min(repeat, 50))
	case "z":
		v.prefix = "z"
		return
	case "i":
		if prefix == "z" {
			v.zoomTo(v.zoom + 10*min(repeat, 50))
		}
	case "o":
		if prefix == "z" {
			v.zoomTo(v.zoom - 10*min(repeat, 50))
		} else {
			v.prompt = ":open "
		}
	case "a":
		v.zoomTo(0)
	case "s":
		v.zoomTo(100)
	case "r", "R":
		v.open(v.file)
	case "m", "'":
		v.prefix = key
		return
	case ":":
		v.prompt = ":"
	case "/", "?":
		v.prompt = key
	case "n":
		v.find(true, false)
	case "N":
		v.find(true, true)
	case "\x0c":
		v.lastStatus = ""
	default:
		if prefix == "z" && key == "\r" {
			v.zoomTo(n)
		}
	}
	v.count = ""
}

func (v *viewer) help() {
	v.message = "Tab contents | S-Tab selectable text | j/k scroll | J/K page | +/- zoom | / search | q quit"
}

func (v *viewer) find(next, reverse bool) {
	if v.search == "" {
		v.message = "Use /text or ?text first"
		return
	}
	if len(v.search) > limits.PreviewSearchBytes {
		v.message = fmt.Sprintf("Search is limited to %d bytes", limits.PreviewSearchBytes)
		return
	}
	if v.pages == 0 {
		v.message = "PDF is still loading"
		return
	}
	back := v.searchBackward != reverse
	direction := "forward"
	if back {
		direction = "backward"
	}
	if next {
		if back {
			direction = "previous"
		} else {
			direction = "next"
		}
	}
	v.command("find " + direction + " " + quoteDSL(v.search))
	v.message = "Searching..."
}

func (v *viewer) execute(command string) {
	name, arg, _ := strings.Cut(command, " ")
	arg = strings.TrimSpace(arg)
	n, numberErr := strconv.Atoi(arg)
	switch name {
	case "q", "quit", "exit":
		v.quit = true
	case "help", "h":
		v.help()
	case "reload", "r":
		v.open(v.file)
	case "open", "o":
		if len(arg) >= 2 && (arg[0] == '\'' || arg[0] == '"') && arg[len(arg)-1] == arg[0] {
			arg = arg[1 : len(arg)-1]
		}
		if arg == "" {
			v.message = "Usage: :open path.pdf"
			return
		}
		path, err := validatePDF(arg)
		if err != nil {
			v.message = err.Error()
			return
		}
		v.marks = make(map[string]int)
		v.open(path)
		v.jump("position", 0)
	case "page", "p":
		if numberErr != nil || n < 1 {
			v.message = "Usage: :page NUMBER"
			return
		}
		v.jump("page", min(n, 1000000))
	case "zoom":
		if arg == "width" {
			v.zoomTo(100)
		} else if arg == "fit" {
			v.zoomTo(0)
		} else if numberErr == nil && n >= 10 && n <= 500 {
			v.zoomTo(n)
		} else {
			v.message = "Usage: :zoom 10..500|width|fit"
		}
	case "":
	default:
		if page, err := strconv.Atoi(name); err == nil && page > 0 {
			v.jump("page", min(page, 1000000))
		} else {
			v.message = "Unknown command: " + name
		}
	}
}

// Replies are framed independently of keystrokes, even when fragmented by a PTY.
func (v *viewer) reply(payload string) {
	if strings.HasPrefix(payload, "region secondary data ") {
		v.metadataReply(payload)
		return
	}
	f := strings.Fields(payload)
	if len(f) == 4 && strings.Join(f[:3], " ") == "region secondary found" {
		if page, err := strconv.Atoi(f[3]); err == nil && page >= 0 {
			if page == 0 {
				v.message = "Text not found"
			} else {
				v.message = fmt.Sprintf("Text found on page %d (n/N: next/previous matching page)", page)
			}
		}
		return
	}
	if len(f) != 9 || strings.Join(f[:3], " ") != "region secondary status" {
		return
	}
	var nums [6]int
	for i := range nums {
		n, err := strconv.Atoi(f[i+3])
		if err != nil || n < 0 {
			return
		}
		nums[i] = n
	}
	if nums[3] < 10 || nums[3] > 500 || nums[2] > 1000000 || nums[4] > 1 || nums[5] > 1 {
		return
	}
	previousNotice := v.panelNotice()
	if v.panel == panelText && v.page != nums[0] {
		v.panelDirty = true
	}
	v.page, v.pages, v.position, v.zoom = nums[0], nums[1], nums[2], nums[3]
	v.loading, v.failed = nums[4] != 0, nums[5] != 0
	v.syncPanel()
	if v.panelNotice() != previousNotice {
		v.panelDirty = true
	}
}

func (v *viewer) draw() {
	v.drawPanel()
	text := fmt.Sprintf(" %d/%d  %d%%  [%d%%]  %s", v.page, v.pages, v.position/10000, v.zoom, filepath.Base(v.file))
	if v.loading {
		text += "  loading..."
	}
	if v.failed {
		text += "  PDF load/render failed (:reload to retry)"
	}
	if v.message != "" {
		text = " " + v.message
	}
	if v.count != "" || v.prefix != "" {
		text += "  " + v.count + v.prefix
	}
	if v.prompt != "" {
		text = v.prompt
	}
	text = statusText(text, max(1, v.cols-1))
	if text == v.lastStatus {
		return
	}
	v.lastStatus = text
	v.write("\x1b[H\x1b[0;38;5;252;48;5;236m\x1b[2K" + text + "\x1b[0m")
}

// Restrict chrome to single-cell printable ASCII; filenames cannot inject VT
// controls or make a wide/combining character wrap the one-row terminal grid.
func statusText(s string, width int) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= width {
			break
		}
		if r < 32 || r > 126 {
			r = '?'
		}
		b.WriteByte(byte(r))
	}
	return b.String()
}
