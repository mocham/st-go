// gov plays local MP4 files using st-fb's bundled decoder and compositor.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type options struct {
	file   string
	muted  bool
	volume int
}

func parseOptions(args []string, out io.Writer) (options, error) {
	var o options
	f := flag.NewFlagSet("gov", flag.ContinueOnError)
	f.SetOutput(out)
	f.BoolVar(&o.muted, "mute", false, "start muted")
	f.IntVar(&o.volume, "volume", 100, "playback volume (0..100)")
	f.Usage = func() {
		fmt.Fprintln(out, "Usage: gov [-mute] [-volume 0..100] movie.mp4\n\nSpace pause/resume; Left/Right seek 5s; click progress bar to seek; [/] speed; 0 restart; +/- volume; m mute; q quit.")
	}
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 1 {
		return o, fmt.Errorf("one MP4 filename is required")
	}
	if o.volume < 0 || o.volume > 100 {
		return o, fmt.Errorf("volume must be 0..100")
	}
	path, err := filepath.Abs(f.Arg(0))
	if err != nil {
		return o, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return o, err
	}
	if !info.Mode().IsRegular() {
		return o, fmt.Errorf("video must be a regular local file")
	}
	o.file = path
	return o, nil
}
func main() {
	o, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		err = run(o, os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gov:", err)
		os.Exit(1)
	}
}
