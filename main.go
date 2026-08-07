package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"st-go/config"
	"st-go/ptyutil"
	"st-go/term"
)

// Version is reported by the -v flag. Tools such as fastfetch parse it.
const Version = "0.9.2"

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "config.json", "path to JSON config")
	var shell string
	flag.StringVar(&shell, "e", "", "command to execute")
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
	flag.Parse()

	if version {
		fmt.Printf("%s %s\n", "st", Version)
		os.Exit(0)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("config: %v (using defaults)", err)
		cfg = config.Default()
	}

	if dumpText != "" {
		dumpShellText(cfg, shell, dumpText)
		return
	}

	if ratio <= 0 {
		log.Fatalf("invalid --ratio value: %v", ratio)
	}

	// the render size drives both the FreeType pixel size and the cell
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

	t, err := NewTerminalRatio(cfg, ratio)
	if err != nil {
		log.Fatalf("x11: %v", err)
	}
	defer t.Close()
	t.loadInputConfig(cfg)

	core := term.NewTerm(cfg, t)
	t.termCore = core

	// pty
	master, slave, err := ptyutil.Open()
	if err != nil {
		log.Fatalf("pty: %v", err)
	}
	defer master.Close()

	prog, env, err := ResolveShell(cfg, shell)
	if err != nil {
		log.Fatalf("shell: %v", err)
	}
	child, err := ptyutil.Start(slave, []string{prog}, env)
	if err != nil {
		log.Fatalf("spawn: %v", err)
	}
	defer func() {
		if child.Process != nil {
			child.Process.Kill()
		}
	}()
	go func() {
		child.Wait()
		os.Exit(0)
	}()

	core.SetWriter(func(b []byte) {
		master.Write(b)
	})
	ptyutil.SetWinSize(master, int(cfg.Rows), int(cfg.Cols))

	// pty reader goroutine; terminal access is serialized with t.mu
	go func() {
		r := bufio.NewReader(master)
		buf := make([]byte, 8192)
		var pending []byte
		for {
			n, err := r.Read(buf)
			if err != nil {
				os.Exit(0)
			}
			t.mu.Lock()
			pending = append(pending, buf[:n]...)
			written := core.Twrite(pending, false)
			pending = pending[written:]
			core.Redraw()
			t.mu.Unlock()
		}
	}()

	core.Redraw()
	t.run(core)
}

// dumpShellText runs the configured shell through a pty and the term core
// (no X window), printing the first screen rows as text. Used for debugging
// what the shell actually sends (e.g. PS1 expansion).
func dumpShellText(cfg *config.Config, shell string, outFile string) {
	trm := &Terminal{}
	core := term.NewTerm(cfg, trm)
	trm.termCore = core

	master, slave, err := ptyutil.Open()
	if err != nil {
		log.Fatalf("pty: %v", err)
	}
	defer master.Close()

	cmdline := shell
	prog, env, err := ResolveShell(cfg, cmdline)
	if err != nil {
		log.Fatalf("shell: %v", err)
	}
	child, err := ptyutil.Start(slave, []string{prog}, env)
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
