// st-client presents an st-web service and its terminal tabs locally.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, in *os.File, out, stderr io.Writer) int {
	flags := flag.NewFlagSet("st-client", flag.ContinueOnError)
	flags.SetOutput(stderr)
	list := flags.Bool("list", false, "list terminals at a service URL")
	id := flags.String("pty", "", "initial service PTY tab")
	create := flags.Bool("new", false, "create and attach a service PTY")
	directory := flags.String("dir", "", "remote working directory for new tabs")
	title := flags.String("title", "", "remote title for new tabs")
	reconnect := flags.Bool("reconnect", true, "reattach after a broken connection")
	clipboardRead := flags.Bool("clipboard-read", false, "answer remote clipboard reads using local OSC 52")
	sshHost := flags.String("ssh", "", "connect through OpenSSH to host or user@host (URL is remote; default http://127.0.0.1:8080/)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: st-client [options] URL\n       st-client -ssh [user@]host [options] [URL]\n\nConnect from st-go X11/fb. Ctrl-] then n creates a tab; 1-9 selects; x closes; q detaches.\nSSH: Ctrl-] u uploads, d downloads, k cancels a transfer.\nST_CLIENT_AUTHORIZATION supplies the complete HTTP Authorization header.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 || (flags.NArg() == 0 && *sshHost == "") || (*list && (*create || *id != "")) || (*create && *id != "") {
		flags.Usage()
		return 2
	}
	address := flags.Arg(0)
	if address == "" {
		address = "http://127.0.0.1:8080/"
	}
	r, err := newRemote(address, os.Getenv("ST_CLIENT_AUTHORIZATION"))
	if err != nil {
		fmt.Fprintln(stderr, "st-client:", err)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	if *sshHost != "" {
		tunnel, err := newSSHTunnel(*sshHost, r)
		if err != nil {
			fmt.Fprintln(stderr, "st-client:", err)
			return 1
		}
		defer tunnel.close()
		defer r.http.CloseIdleConnections()
		startup, stop := context.WithTimeout(ctx, 2*time.Minute)
		err = tunnel.ensure(startup, in, stderr)
		stop()
		if err != nil {
			fmt.Fprintln(stderr, "st-client:", err)
			return 1
		}
	}
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	service, preferred := r.serviceTarget()
	if service == nil && (*list || *create || *id != "") {
		fmt.Fprintln(stderr, "st-client: -list, -new and -pty require a managed service URL")
		return 2
	}
	if *id != "" {
		preferred = *id
	}
	if *list {
		sessions, err := service.sessions(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "st-client:", err)
			return 1
		}
		for _, p := range sessions {
			fmt.Fprintf(out, "%s\t%t\t%s\n", cleanText(p.ID), p.Occupied, cleanText(p.Title))
		}
		return 0
	}
	fd := int(in.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		fmt.Fprintln(stderr, "st-client: stdin must be a terminal:", err)
		return 1
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err = unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	result := func() error {
		defer unix.IoctlSetTermios(fd, unix.TCSETS, old)
		defer writeAll(out, []byte("\x18\x1b[?2026l\x1bPrefresh 0 0\x1b\\\x1bPpdfsearch clear\x1b\\\x1bPregion reset\x1b\\\x1b[?1003l\x1b[?1002l\x1b[?1000l\x1b[?1006l\x1b[?1004l\x1b[?2004l\x1b[?7h\x1b[0 q\x1b[0m\x1b[?25h\x1b[?1049l"))
		if err := writeAll(out, []byte("\x1b[?1049h\x1b[?7l\x1b[?2004h\x1b[?1003h\x1b[?1006h\x1b[?1004h\x1b[?25l\x1b[2J")); err != nil {
			return err
		}
		a := &app{remote: r, out: out, fd: fd, reconnect: *reconnect, clipboardRead: *clipboardRead}
		if service != nil {
			a.tabs = &serviceTabs{remote: service, preferred: preferred, startNew: *create, directory: *directory, title: *title}
		}
		return a.run(ctx, signals)
	}()
	if result != nil {
		fmt.Fprintln(stderr, "st-client:", strings.TrimSpace(result.Error()))
		return 1
	}
	return 0
}

type readResult struct {
	data []byte
	err  error
}

// A blocking, interruptible poll owns input. It does not leave an os.Stdin.Read
// goroutine alive after detach and needs no periodic idle wakeup.
func readInput(ctx context.Context, fd int, results chan<- readResult) (func(), error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { w.Write([]byte{1}) })
	go func() {
		defer close(done)
		var buf [65536]byte
		for {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}, {Fd: int32(r.Fd()), Events: unix.POLLIN}}
			_, err := unix.Poll(poll, -1)
			if err == unix.EINTR {
				continue
			}
			if ctx.Err() != nil || poll[1].Revents != 0 {
				return
			}
			var data []byte
			if err == nil && poll[0].Revents&unix.POLLIN != 0 {
				n, e := unix.Read(fd, buf[:])
				err = e
				if err == unix.EINTR || err == unix.EAGAIN {
					continue
				}
				if n > 0 {
					data = append([]byte(nil), buf[:n]...)
				} else if err == nil {
					err = io.EOF
				}
			}
			if len(data) == 0 && err == nil {
				err = io.EOF
			}
			select {
			case results <- readResult{data, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return func() { stop(); w.Write([]byte{1}); <-done; r.Close(); w.Close() }, nil
}
