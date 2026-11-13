// gxw is a small, non-reparenting, tabbed X11 window manager.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

type options struct {
	display, terminal, fontPath, screenshotPath string
	configPath                                  string
	fontSize                                    float64
	noAutostart                                 bool
	vt                                          int
}

func main() {
	log.SetPrefix("gxw: ")
	log.SetFlags(0)
	var o options
	// xinit alone invokes this private mode. Normal invocations always own
	// session startup and reject an existing graphical environment.
	child := len(os.Args) > 1 && os.Args[1] == "--gxw-session" && os.Getenv("GXW_XINIT_CHILD") == "1"
	if child {
		args := os.Args[2:]
		if raw := os.Getenv("GXW_SESSION_ARGS"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				log.Fatal("invalid internal session arguments")
			}
		}
		os.Args = append(os.Args[:1], args...)
		os.Unsetenv("GXW_XINIT_CHILD")
		os.Unsetenv("GXW_SESSION_ARGS")
	}
	flag.StringVar(&o.display, "display", "", "local X display to start (default: next available)")
	flag.IntVar(&o.vt, "vt", 0, "Linux virtual terminal (default: current text console)")
	flag.StringVar(&o.terminal, "terminal", "", "terminal command, interpreted by /bin/sh")
	flag.StringVar(&o.configPath, "config", "", "st JSON config (default: <exe-dir>/st.json, then config.json, else embedded)")
	flag.StringVar(&o.fontPath, "font", "", "TrueType/TTC font file (overrides the st config font)")
	flag.StringVar(&o.screenshotPath, "screenshot", "/tmp/gxw-screenshot.png", "screenshot output path")
	flag.Float64Var(&o.fontSize, "font-size", 16, "base tab font size in pixels (10..40; doubled above 1200x800)")
	flag.BoolVar(&o.noAutostart, "no-autostart", false, "do not launch an initial terminal")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: gxw [options] [-- initial-command args...]\nRun directly from a Linux text console. Starts xinit without user session scripts.")
		flag.PrintDefaults()
	}
	flag.Parse()
	if o.display != "" {
		var err error
		o.display, err = localDisplay(o.display)
		if err != nil {
			log.Fatal(err)
		}
	}
	if o.fontSize < 10 || o.fontSize > 40 || math.IsNaN(o.fontSize) {
		log.Fatal("-font-size must be between 10 and 40")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if !child {
		if err := launchSession(ctx, o, os.Args[1:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	w, err := newWM(o)
	if err != nil {
		log.Fatal(err)
	}
	defer w.close()
	if args := flag.Args(); len(args) > 0 {
		w.launch(args)
	} else if !o.noAutostart {
		w.launchTerminal()
	}
	if err := w.run(ctx); err != nil {
		log.Print(err)
	}
}

func defaultTerminal() []string {
	if self, err := os.Executable(); err == nil {
		for _, name := range []string{"st", "st-min"} {
			path := filepath.Join(filepath.Dir(self), name)
			if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
				return []string{path}
			}
		}
	}
	for _, name := range []string{"st", "st-min", "xterm", "x-terminal-emulator"} {
		if path, err := exec.LookPath(name); err == nil {
			return []string{path}
		}
	}
	return nil
}

func (w *wm) launchTerminal() {
	args := defaultTerminal()
	if w.options.terminal != "" {
		args = []string{"/bin/sh", "-c", w.options.terminal}
	}
	if len(args) == 0 {
		w.notice("No terminal found; use -terminal command")
		return
	}
	w.launch(args)
}

func (w *wm) launch(args []string) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = os.Environ()
	if w.options.display != "" {
		cmd.Env = append(cmd.Env, "DISPLAY="+w.options.display)
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		w.notice(err.Error())
		return
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("%s: %v", args[0], err)
		}
	}()
}
