package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type scaleMode uint8

const (
	fitWidth scaleMode = iota
	fitHeight
	originalSize
	customZoom
)

type viewer struct {
	out                         io.Writer
	err                         error
	images                      []string
	index, cols, zoom, position int
	mode                        scaleMode
	loading, failed, quit       bool
	message, lastStatus         string
}

func quoteDSL(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func imageFiles(path string) ([]string, int, error) {
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}
	images := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !isImageName(entry.Name()) || !protocolPath(entry.Name()) {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		info, err := os.Stat(full)
		if err == nil && info.Mode().IsRegular() {
			images = append(images, full)
		}
	}
	sort.SliceStable(images, func(i, j int) bool {
		a, b := strings.ToLower(filepath.Base(images[i])), strings.ToLower(filepath.Base(images[j]))
		if a == b {
			return images[i] < images[j]
		}
		return a < b
	})
	for i, image := range images {
		if image == path {
			return images, i, nil
		}
	}
	return nil, 0, fmt.Errorf("image is not in its directory listing")
}

func newViewer(o options, out io.Writer) (*viewer, error) {
	images, index, err := imageFiles(o.file)
	if err != nil {
		return nil, err
	}
	return &viewer{out: out, images: images, index: index, cols: 80, zoom: 100, mode: fitWidth}, nil
}

func (v *viewer) write(s string) {
	if v.err == nil {
		_, v.err = io.WriteString(v.out, s)
	}
}

func (v *viewer) command(s string) { v.write("\x1bPregion secondary " + s + "\x1b\\") }

func (v *viewer) current() string { return v.images[v.index] }

func (v *viewer) open() {
	mode := "fit-width"
	switch v.mode {
	case fitHeight:
		mode = "fit-height"
	case originalSize:
		mode = "native"
	case customZoom:
		mode = fmt.Sprintf("zoom %d", v.zoom)
	}
	v.command("open " + quoteDSL(v.current()) + " viewer " + mode)
	v.loading, v.failed, v.position = true, false, 0
	v.lastStatus = ""
}

func (v *viewer) fit(mode scaleMode) {
	v.mode = mode
	switch mode {
	case fitHeight:
		v.command("fit height")
	case originalSize:
		v.command("fit original")
	default:
		v.command("fit width")
	}
	v.lastStatus = ""
}

func (v *viewer) next(delta int) {
	if len(v.images) == 0 {
		return
	}
	v.index = (v.index + delta + len(v.images)) % len(v.images)
	v.message = ""
	v.open()
}

func (v *viewer) key(key string) {
	v.message = ""
	switch key {
	case "q", "escape", "\x03":
		v.quit = true
	case "w":
		v.fit(fitWidth)
	case "a":
		v.fit(fitHeight)
	case "o":
		v.fit(originalSize)
	case "n":
		v.next(1)
	case "p":
		v.next(-1)
	case "ctrl-plus":
		v.mode = customZoom
		v.command("zoomstep 1")
	case "ctrl-minus":
		v.mode = customZoom
		v.command("zoomstep -1")
	case "up":
		v.command("scroll -1")
	case "down":
		v.command("scroll 1")
	case "left":
		v.command("pan -1")
	case "right":
		v.command("pan 1")
	case "pageup":
		v.command("view -1000000")
	case "pagedown", " ":
		v.command("view 1000000")
	case "r":
		v.open()
	case "?", "h":
		v.message = "w width | a height | o original | n/p image | Ctrl+wheel/+/- zoom | drag pan | q quit"
	}
}

func (v *viewer) reply(payload string) {
	f := strings.Fields(payload)
	if len(f) == 10 && strings.Join(f[:3], " ") == "region secondary image-status" {
		var values [6]int
		for i, field := range []string{f[3], f[4], f[5], f[6], f[8], f[9]} {
			n, err := strconv.Atoi(field)
			if err != nil || n < 0 {
				return
			}
			values[i] = n
		}
		if values[2] > 1000000 || values[4] > 1 || values[5] > 1 {
			return
		}
		mode := map[string]scaleMode{"width": fitWidth, "height": fitHeight, "original": originalSize, "zoom": customZoom}[f[7]]
		if f[7] != "width" && mode == fitWidth {
			return
		}
		v.mode, v.position, v.zoom = mode, values[2], max(1, values[3])
		v.loading, v.failed = values[4] != 0, values[5] != 0
		return
	}
	if len(f) != 9 || strings.Join(f[:3], " ") != "region secondary status" {
		return
	}
	var values [6]int
	for i := range values {
		n, err := strconv.Atoi(f[i+3])
		if err != nil || n < 0 {
			return
		}
		values[i] = n
	}
	if values[2] > 1000000 || values[4] > 1 || values[5] > 1 {
		return
	}
	v.position, v.zoom = values[2], max(1, values[3])
	v.loading, v.failed = values[4] != 0, values[5] != 0
}

func (v *viewer) modeName() string {
	return [...]string{"width", "height", "original", "zoom"}[v.mode]
}

func (v *viewer) draw() {
	text := fmt.Sprintf(" %d/%d  %s  %d%%  [%d%%]  %s", v.index+1, len(v.images), v.modeName(), v.zoom, v.position/10000, filepath.Base(v.current()))
	if v.loading {
		text += "  loading..."
	}
	if v.failed {
		text += "  image load/render failed (r to retry)"
	}
	if v.message != "" {
		text = " " + v.message
	}
	text = statusText(text, max(1, v.cols-1))
	if text == v.lastStatus {
		return
	}
	v.lastStatus = text
	v.write("\x1b[H\x1b[0;38;5;252;48;5;236m\x1b[2K" + text + "\x1b[0m")
}

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
