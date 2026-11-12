package frontend

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"st-go/config"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
	"st-go/ptyutil"
	"st-go/terminfo"
)

// Version is reported by the -v flag. Tools such as fastfetch parse it.
const Version = "0.9.2"

// Main wires the native backend without making the shared host depend on it.
func Main(open api.OpenHost, font []byte) {
	openWindow = open
	embeddedFont = font
	defer closeFonts()
	main()
}

func main() {
	vimMode, vimOptions, vimFile, err := parseVimInvocation(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "", "path to JSON config (default: <exe-dir>/st.json, then config.json, else embedded)")
	var glyphOut string
	flag.StringVar(&glyphOut, "glyphtest", "", "render a glyph to PPM and exit")
	var dumpText string
	flag.StringVar(&dumpText, "dumptext", "", "run shell via pty and print screen text (no X)")
	var ratio float64
	flag.Float64Var(&ratio, "ratio", 1.0, "multiply glyph geometry defaults (cell width, height, baseline)")
	var version bool
	flag.BoolVar(&version, "v", false, "print version and exit")
	var fontFile string
	flag.StringVar(&fontFile, "f", "", "use the given .ttf/.otf font file instead of the embedded one")
	var title string
	flag.StringVar(&title, "t", "", "window title")
	flag.StringVar(&title, "T", "", "window title")
	var name string
	flag.StringVar(&name, "n", "", "window name")
	var class string
	flag.StringVar(&class, "c", "", "window class")
	var geometry string
	flag.StringVar(&geometry, "g", "", "geometry WxH+X+Y (columns x rows + x + y)")
	var noAlt bool
	flag.BoolVar(&noAlt, "a", false, "disable the alternate screen buffer")
	var fixed bool
	flag.BoolVar(&fixed, "i", false, "keep the window at a fixed size")
	var directory string
	flag.StringVar(&directory, "dir", "", "working directory for the terminal session")
	var installTerminfo bool
	flag.BoolVar(&installTerminfo, "install-terminfo", false, "generate and install st-256color, then exit")
	var allowTabs bool
	flag.BoolVar(&allowTabs, "tabs", tabsCompiled, "allow applications to initialize terminal tabs lazily")
	var noTabs bool
	flag.BoolVar(&noTabs, "no-tabs", false, "disable terminal tab requests")

	// -e consumes ALL remaining arguments as the command + its args
	// (st: `goto run; opt_cmd = argv`). Split the args at -e.
	var cmdArgs []string
	var preArgs []string
	args := os.Args[1:]
	if !vimMode {
		for i, a := range args {
			if a == "-e" || a == "--e" {
				cmdArgs = args[i+1:]
				preArgs = args[:i]
				break
			}
		}
		if cmdArgs == nil {
			preArgs = args
		}
	}
	os.Args = append([]string{os.Args[0]}, preArgs...)
	flag.Parse()

	if version {
		fmt.Printf("%s %s\n", "st", Version)
		os.Exit(0)
	}

	cfg, err := config.LoadResolved(cfgPath)
	if err != nil {
		log.Printf("config: %v (using embedded)", err)
		cfg = config.Default()
	}
	if installTerminfo {
		path, err := terminfo.Install("", cfg.Cols, cfg.Rows, cfg.Tabspaces)
		if err != nil {
			log.Fatalf("terminfo: %v", err)
		}
		fmt.Println(path)
		return
	}
	if token := newGeometryToken(); token != "" {
		cfg.GeometryToken = token
	} else {
		cfg.AllowGeometryOps = false
		log.Printf("geometry operations disabled: capability token generation failed")
	}

	if noAlt {
		cfg.AllowAltScreen = false
	}
	childDir := ""
	if directory != "" {
		childDir, err = filepath.Abs(directory)
		if err != nil {
			log.Fatalf("directory: %v", err)
		}
		info, statErr := os.Stat(childDir)
		if statErr != nil || !info.IsDir() {
			log.Fatalf("directory %q is not available", directory)
		}
	}
	if vimMode {
		spec, err := buildVimLaunch(vimOptions, vimFile)
		if err != nil {
			log.Fatalf("vim: %v", err)
		}
		cmdArgs = spec.Command
		childDir = spec.Dir
		if title == "" {
			title = spec.Title
		}
	}
	// -n name sets the instance (res_name); -c sets the class (res_class)
	className := cfg.Termname
	if class != "" {
		className = class
	}
	instanceName := cfg.Termname
	if name != "" {
		instanceName = name
	}

	// -g geometry: cols x rows + x + y (follows st/XParseGeometry)
	var gx, gy int
	var geomMask XGeometryMask
	if geometry != "" {
		cfgCols := int(cfg.Cols)
		cfgRows := int(cfg.Rows)
		geomMask = parseGeometry(geometry, &cfgCols, &cfgRows, &gx, &gy)
		if geomMask&WidthValue != 0 {
			cfg.Cols = uint(cfgCols)
		}
		if geomMask&HeightValue != 0 {
			cfg.Rows = uint(cfgRows)
		}
		if geomMask&XValue == 0 {
			gx = 0
		}
		if geomMask&YValue == 0 {
			gy = 0
		}
	}

	// default title: the -e command name if given, else "tile_terminal" (st)
	if title == "" {
		if len(cmdArgs) > 0 {
			title = cmdArgs[0]
		} else {
			title = "tile_terminal"
		}
	}

	if dumpText != "" {
		dumpShellText(cfg, cmdArgs, dumpText)
		return
	}

	if ratio <= 0 {
		log.Fatalf("invalid --ratio value: %v", ratio)
	}

	// the render size drives both the font rasterizer's pixel size and the cell
	// height, so characters fill their (scaled) cell.
	renderSize := int(float64(cfg.GlyphHeight) * ratio)
	if renderSize <= 0 {
		renderSize = 16
	}
	// -f overrides the embedded Monaco_Linux.ttf; otherwise the embedded
	// font (go:embed) is used.
	font := ""
	if fontFile != "" {
		font = fontFile
	} else if cfg.Font != "" && cfg.Font != "Monaco_Linux.ttf" {
		// a custom config font still wins over the embedded default
		font = cfg.Font
	}
	if !loadFonts(font, renderSize) {
		log.Printf("warning: font load failed; glyphs may be blank")
	}

	if glyphOut != "" {
		renderGlyphToFile(cfg, 'A', 0xFFadd8e6, 0xFF181818, glyphOut)
		return
	}

	t, err := NewTerminalOpts(cfg, ratio, gx, gy, geomMask, title, instanceName, className, fixed)
	if err != nil {
		log.Fatalf("display: %v", err)
	}
	defer t.Close()
	if service, ok := t.host.(api.SessionService); ok {
		if err := serveTerminalSessions(service, cfg, ratio, cmdArgs, childDir, title, allowTabs && !noTabs); err != nil {
			log.Printf("terminal service: %v", err)
		}
		return
	}
	if identity, ok := t.host.(api.BackendIdentity); ok && identity.BackendName() == "x11" {
		cfg.ClipboardSyncToken = newGeometryToken()
	}
	cfg.NativeAudioToken = newGeometryToken()
	if _, ok := t.host.(api.NativeVideoHost); ok {
		cfg.NativeVideoToken = newGeometryToken()
	}
	configureTerminalTabs(t, allowTabs && !noTabs)
	t.loadInputConfig(cfg)
	if vimMode {
		t.lockTitle = true
		screenW, screenH := t.host.ScreenSize()
		bottomReserve := screenH / 10
		if bottomReserve < 64 {
			bottomReserve = 64
		}
		t.requestWindowRect(emulatedVimRect(
			screenW, screenH,
			t.cw+2*t.borderpx, t.ch+2*t.borderpx, bottomReserve))
	}

	core := term.NewTerm(cfg, t)
	if childDir != "" {
		core.SetWorkingDirectory(childDir)
	}
	t.attachTerm(core)
	t.setupKeys()
	t.mu.Lock()
	// Console hosts have a physical size independent of the configured grid.
	// Match it before spawning the child, not after its first output/resize.
	if w, h, err := t.host.ClientSize(); err == nil {
		t.resize(w, h)
	}
	core.Redraw()
	t.mu.Unlock()

	// spawn: -e cmd args... runs that command; else resolve the shell
	var cmdline []string
	var env []string
	if len(cmdArgs) > 0 {
		// st's execsh: prog = args[0], execvp(prog, args)
		cmdline = cmdArgs
		env = stChildEnv(cfg)
	} else {
		var prog string
		prog, env, err = ResolveShell(cfg, "")
		if err != nil {
			t.Close()
			log.Fatalf("shell: %v", err)
		}
		cmdline = []string{prog}
	}
	env = backendChildEnv(env, t.host)
	if childDir != "" {
		env = append(envWithout(env, "PWD"), "PWD="+childDir)
	}
	if err := t.StartSession(SessionOptions{Command: cmdline, Env: env, Directory: childDir}); err != nil {
		t.Close()
		log.Fatalf("spawn: %v", err)
	}
	if t.tabs != nil {
		t.tabs.run(t)
	} else {
		t.run()
	}
}

func durationMilliseconds(ms float64) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms * float64(time.Millisecond))
}

func pollMilliseconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Millisecond - 1) / time.Millisecond)
}

func newGeometryToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// dumpShellText runs the configured shell through a pty and the term core
// (no X window), printing the first screen rows as text. Used for debugging
// what the shell actually sends (e.g. PS1 expansion).
func dumpShellText(cfg *config.Config, cmdArgs []string, outFile string) {
	trm := &Terminal{}
	core := term.NewTerm(cfg, trm)
	trm.termCore = core

	master, slave, err := ptyutil.Open()
	if err != nil {
		log.Fatalf("pty: %v", err)
	}
	defer master.Close()

	var cmdline []string
	var env []string
	if len(cmdArgs) > 0 {
		cmdline = cmdArgs
		env = stChildEnv(cfg)
	} else {
		var prog string
		prog, env, err = ResolveShell(cfg, "")
		if err != nil {
			log.Fatalf("shell: %v", err)
		}
		cmdline = []string{prog}
	}
	child, err := ptyutil.Start(slave, cmdline, env)
	if err != nil {
		log.Fatalf("spawn: %v", err)
	}
	defer func() {
		if child.Process != nil {
			child.Process.Kill()
		}
	}()
	core.SetWriter(func(b []byte) {
		master.Write(b)
	})
	ptyutil.SetWinSize(master, int(cfg.Rows), int(cfg.Cols))

	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			if err != nil {
				return
			}
			core.Twrite(buf[:n], false)
		}
	}()

	time.Sleep(1200 * time.Millisecond)
	core.Redraw()
	var out strings.Builder
	for y := 0; y < 3 && y < core.Rows(); y++ {
		out.WriteString(core.LineText(y))
		out.WriteString("\n")
	}
	if outFile == "-" {
		os.Stdout.WriteString(out.String())
	} else {
		os.WriteFile(outFile, []byte(out.String()), 0644)
	}
}
