//go:build linux

// govim-pinyind is a bounded pure-Go input-method worker.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"st-go/demo-go/pinyind/internal/pinyinengine"
	"st-go/tui/pinyin"
)

func run(args []string) error {
	flags := flag.NewFlagSet("govim-pinyind", flag.ContinueOnError)
	address := flags.String("socket", "", "private Linux abstract Unix socket")
	parent := flags.Int("parent", 0, "expected owner PID")
	license := flags.Bool("license", false, "print decoder/dictionary license notices")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *license {
		fmt.Print(pinyinengine.License())
		return nil
	}
	if flags.NArg() != 0 || !strings.HasPrefix(*address, "@govim-pinyin-") || len(*address) > 100 || *parent < 1 {
		return fmt.Errorf("requires --socket @govim-pinyin-... --parent PID")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: *address, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	listener.SetDeadline(time.Now().Add(3 * time.Second))
	var conn *net.UnixConn
	for {
		candidate, err := listener.AcceptUnix()
		if err != nil {
			return err
		}
		if err := pinyin.CheckPeer(candidate, *parent); err != nil {
			candidate.Close()
			continue
		}
		conn = candidate
		break
	}
	defer conn.Close()
	listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	requests := make(chan pinyin.Request, 1)
	lost := make(chan struct{})
	go func() {
		defer close(lost)
		scanner := pinyin.Scanner(conn)
		for {
			var r pinyin.Request
			if pinyin.Read(scanner, &r) != nil {
				return
			}
			if r.Validate() != nil {
				return
			}
			select {
			case requests <- r:
			case <-ctx.Done():
				return
			}
		}
	}()
	queries := make(chan pinyin.Request)
	answers := make(chan pinyin.Response, 1)
	go func() {
		engine, err := pinyinengine.Open()
		if err != nil {
			answers <- pinyin.Response{Error: err.Error()}
			return
		}
		defer engine.Close()
		answers <- pinyin.Response{Ready: true}
		for {
			select {
			case <-ctx.Done():
				return
			case <-lost:
				return
			case r := <-queries:
				response, err := engine.Lookup(r)
				if err != nil {
					response.Error = err.Error()
				}
				select {
				case answers <- response:
				case <-ctx.Done():
					return
				case <-lost:
					return
				}
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil
	case <-lost:
		return nil
	case ready := <-answers:
		if err := pinyin.Write(conn, ready); err != nil {
			return err
		}
		if !ready.Ready {
			return fmt.Errorf("Pinyin initialization failed")
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-lost:
			return nil
		case request := <-requests:
			select {
			case queries <- request:
			case <-ctx.Done():
				return nil
			case <-lost:
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-lost:
				return nil
			case response := <-answers:
				if err := pinyin.Write(conn, response); err != nil {
					return err
				}
				if response.Error != "" {
					return fmt.Errorf("Pinyin lookup failed")
				}
			}
		}
	}
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "govim-pinyind:", err)
		os.Exit(1)
	}
}
