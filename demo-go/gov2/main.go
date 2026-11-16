// gov2 is the direct DRM/KMS variant of gov, using the same bundled decoder.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"st-go/platform_neutral/api"
)

type options struct {
	file, device  string
	muted, direct bool
	volume        int
	connector     uint
}

func (o options) request() api.NativeVideoRequest {
	return api.NativeVideoRequest{Player: "gov2", File: o.file, Volume: o.volume, Muted: o.muted, DRMDevice: o.device, Connector: uint32(o.connector)}
}

func parseOptions(args []string, out io.Writer) (options, error) {
	var o options
	f := flag.NewFlagSet("gov2", flag.ContinueOnError)
	f.SetOutput(out)
	f.BoolVar(&o.muted, "mute", false, "start muted")
	f.IntVar(&o.volume, "volume", 100, "playback volume (0..100)")
	f.StringVar(&o.device, "drm-device", "", "DRM card (default: first usable /dev/dri/cardN)")
	f.UintVar(&o.connector, "connector", 0, "active DRM connector ID (default: first usable connector)")
	f.BoolVar(&o.direct, "direct", false, "run on the current real VT; used by st-fb's handoff")
	f.Usage = func() {
		fmt.Fprintln(out, "Usage: gov2 [-mute] [-volume 0..100] [-drm-device /dev/dri/cardN] [-connector ID] movie.mp4\n\nSpace pause/resume; Left/Right seek 5s; [/] speed; 0 restart; +/- volume; m mute; q quit.")
	}
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 1 {
		return o, fmt.Errorf("one MP4 filename is required")
	}
	if uint64(o.connector) > 1<<32-1 {
		return o, fmt.Errorf("invalid connector ID")
	}
	path, err := filepath.Abs(f.Arg(0))
	if err != nil {
		return o, err
	}
	o.file = path
	if err := o.request().Validate(); err != nil {
		return o, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return o, err
	}
	if !info.Mode().IsRegular() {
		return o, fmt.Errorf("video must be a regular local file")
	}
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
		fmt.Fprintln(os.Stderr, "gov2:", err)
		os.Exit(1)
	}
}
