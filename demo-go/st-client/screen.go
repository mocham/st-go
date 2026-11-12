package main

import (
	"fmt"
	"image"
	"io"
	"sort"
	"strings"
	"unicode"

	"st-go/platform_neutral/api"
)

type cell struct {
	text         string
	fg, bg       uint32
	style        uint16
	continuation bool
	media        uint64
}

type retainedScene struct {
	scene
	rows       map[int]api.SceneRow
	fills      []api.SceneFill
	cursor     api.SceneCursor
	background uint32
}

type screen struct {
	cols, rows, cw, ch, border int
	surfaces                   map[uint64]*retainedScene
	previous                   []cell
	force                      bool
	imagePaths                 map[uint64]string
	paintedImages              map[inlineImage]string
}

func (s *screen) reset() {
	s.surfaces = make(map[uint64]*retainedScene)
	s.previous = nil
	s.force = true
	s.paintedImages = nil
}
func (s *screen) size(cols, rows int) error {
	if cols < 1 || rows < 1 || cols > 1000 || rows > 500 || cols*rows > maxCells {
		return fmt.Errorf("terminal dimensions exceed client limits")
	}
	s.cols, s.rows = cols, rows
	s.reset()
	return nil
}
func (s *screen) dimensions() (int, int) { return s.cols*s.cw + 2*s.border, s.rows*s.ch + 2*s.border }
func (s *screen) point(x, y int) (int, int) {
	return s.border + (x-1)*s.cw + s.cw/2, s.border + (y-1)*s.ch + s.ch/2
}
func (s *screen) rect(x, y, w, h int) image.Rectangle {
	if s.cw < 1 || s.ch < 1 {
		return image.Rectangle{}
	}
	// Remote geometry is measured in remote font pixels. Local ANSI cells are
	// the unit of presentation; never use local font pixels as remote coordinates.
	// Select cells by their centres. Sub-cell border fills must not erase the
	// first/last text row or column.
	ceil := func(n, d int) int {
		if n > 0 {
			return (n + d - 1) / d
		}
		return n / d
	}
	x0 := ceil(x-s.border-s.cw/2, s.cw)
	y0 := ceil(y-s.border-s.ch/2, s.ch)
	x1 := ceil(x+w-s.border-s.cw/2, s.cw)
	y1 := ceil(y+h-s.border-s.ch/2, s.ch)
	return image.Rect(x0, y0, x1, y1).Intersect(image.Rect(0, 0, s.cols, s.rows))
}

func validRect(x, y, w, h int) bool {
	return x >= 0 && y >= 0 && w > 0 && h > 0 && x <= 65535 && y <= 65535 && w <= 65535-x && h <= 65535-y
}

func (s *screen) apply(message serverMessage) error {
	width, height := s.dimensions()
	if message.Width != width || message.Height != height {
		return nil
	}
	if len(message.Scenes) > 128 {
		return fmt.Errorf("too many scene surfaces")
	}
	for _, update := range message.Scenes {
		if update.Hidden {
			delete(s.surfaces, update.ID)
			continue
		}
		if !validRect(update.X, update.Y, update.Width, update.Height) || len(update.Update.Rows) > 5000 {
			return fmt.Errorf("invalid scene geometry")
		}
		retained := s.surfaces[update.ID]
		if retained == nil {
			if len(s.surfaces) >= 128 {
				return fmt.Errorf("too many retained scenes")
			}
			retained = &retainedScene{}
			s.surfaces[update.ID] = retained
		}
		retained.scene = update
		if retained.rows == nil || update.Update.Reset {
			retained.rows = make(map[int]api.SceneRow)
			retained.background = update.Update.Background
		}
		// Every wire scene includes the current fills; an omitted list is empty.
		retained.fills = update.Update.Fills
		if len(retained.fills) > 10000 {
			return fmt.Errorf("too many scene fills")
		}
		for _, row := range update.Update.Rows {
			if row.Y < 0 || row.Y >= update.Height || row.Height < 1 || row.Height > 65535 || row.X < 0 || row.X > 65535 || row.Width < 0 || row.Width > 65535 || len(row.Text) > 10000 || len(row.Media) > 64 {
				return fmt.Errorf("invalid scene row")
			}
			for _, run := range row.Text {
				if run.X < 0 || run.X > 65535 || run.Advance < 1 || run.Advance > 65535 || len(run.Text) > 1<<20 {
					return fmt.Errorf("invalid text run")
				}
			}
			retained.rows[row.Y] = row
		}
		for y := range retained.rows {
			if y >= update.Height {
				delete(retained.rows, y)
			}
		}
		if len(retained.rows) > 5000 {
			return fmt.Errorf("too many retained scene rows")
		}
		if update.Update.Cursor != nil {
			retained.cursor = *update.Update.Cursor
		}
		// Retain only normalized rows, not the most recent wire batch as well.
		retained.scene.Update = api.SceneUpdate{}
	}
	bytes := 0
	for _, surface := range s.surfaces {
		for _, row := range surface.rows {
			bytes += 128 + 64*len(row.Media)
			for _, run := range row.Text {
				bytes += 64 + len(run.Text)
			}
		}
	}
	if bytes > maxMessage {
		return fmt.Errorf("retained scene exceeds memory budget")
	}
	return nil
}

func cleanText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '\ufffd'
		}
		return r
	}, text)
}

func (s *screen) ordered() []*retainedScene {
	var list []*retainedScene
	for _, surface := range s.surfaces {
		list = append(list, surface)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Bar != list[j].Bar {
			return !list[i].Bar
		}
		return list[i].ID < list[j].ID
	})
	return list
}

func (s *screen) draw(out io.Writer) error {
	if s.cw == 0 || s.ch == 0 {
		return nil
	}
	canvas := make([]cell, s.cols*s.rows)
	for i := range canvas {
		canvas[i] = cell{text: " ", fg: 0xffdddddd, bg: 0xff000000}
	}
	fill := func(r image.Rectangle, bg uint32) {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				canvas[y*s.cols+x] = cell{text: " ", fg: 0xffdddddd, bg: bg}
			}
		}
	}
	var cursor api.SceneCursor
	cursorX, cursorY := 0, 0
	for _, surface := range s.ordered() {
		clip := s.rect(surface.X, surface.Y, surface.Width, surface.Height)
		fill(clip, surface.background)
		var ys []int
		for y := range surface.rows {
			ys = append(ys, y)
		}
		sort.Ints(ys)
		for _, y := range ys {
			row := surface.rows[y]
			w := row.Width
			if w == 0 {
				w = surface.Width
			}
			band := s.rect(surface.X+row.X, surface.Y+row.Y, w, row.Height).Intersect(clip)
			fill(band, row.Background)
			for _, run := range row.Text {
				x := (surface.X + run.X - s.border + s.cw/2) / s.cw
				y := (surface.Y + row.Y - s.border + s.ch/2) / s.ch
				advance := max(1, (run.Advance+s.cw/2)/s.cw)
				put := func(text string) {
					if image.Pt(x, y).In(band) {
						canvas[y*s.cols+x] = cell{text: cleanText(text), fg: run.Foreground, bg: run.Background, style: run.Style}
						for k := 1; k < advance && x+k < band.Max.X; k++ {
							canvas[y*s.cols+x+k] = cell{continuation: true, bg: run.Background}
						}
					}
					x += advance
				}
				if run.Cluster {
					put(run.Text)
				} else {
					for _, r := range run.Text {
						if x >= band.Max.X {
							break
						}
						put(string(r))
					}
				}
			}
			for _, m := range row.Media {
				if s.imagePaths[m.ID] == "" {
					continue
				}
				if _, ok := s.inline(surface, m); !ok {
					continue
				}
				r := s.rect(surface.X+m.ClipX, surface.Y+m.ClipY, m.ClipWidth, m.ClipHeight).Intersect(band)
				for y := r.Min.Y; y < r.Max.Y; y++ {
					for x := r.Min.X; x < r.Max.X; x++ {
						canvas[y*s.cols+x] = cell{media: m.ID}
					}
				}
			}
		}
		for _, f := range surface.fills {
			if validRect(f.X, f.Y, f.Width, f.Height) {
				fill(s.rect(surface.X+f.X, surface.Y+f.Y, f.Width, f.Height).Intersect(clip), f.Color)
			}
		}
		if !surface.Bar && surface.cursor.Width > 0 && surface.cursor.Height > 0 {
			cursor = surface.cursor
			cursorX = (surface.X + cursor.X - s.border + s.cw/2) / s.cw
			cursorY = (surface.Y + cursor.Y - s.border + s.ch/2) / s.ch
		}
	}
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[?25l")
	if s.force {
		b.WriteString("\x1b[2J")
		s.force = false
		s.paintedImages = nil
	}
	images := make(map[inlineImage]string)
	for _, im := range s.images() {
		path := s.imagePaths[im.media]
		if path == "" {
			continue
		}
		images[im] = path
		r := im.rect
		unchanged := s.paintedImages[im] == path && len(s.previous) == len(canvas)
		if unchanged {
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					i := y*s.cols + x
					if canvas[i].media == im.media && s.previous[i].media != im.media {
						unchanged = false
					}
				}
			}
		}
		if unchanged {
			continue
		}
		fmt.Fprintf(&b, "\x1bPopen %s rect %d %d %d %d %s page %d\x1b\\", quote(path), r.Min.X+1, r.Min.Y+1, r.Dx(), r.Dy(), fitOption(im.fit), im.page)
		// Opening an image writes its whole rectangle. Repaint ordinary text
		// over clipped/overwritten portions, including unchanged spaces.
		if len(s.previous) == len(canvas) {
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					s.previous[y*s.cols+x] = cell{}
				}
			}
		}
	}
	s.paintedImages = images
	last := cell{}
	lastX, lastY := -1, -1
	for y := 0; y < s.rows; y++ {
		for x := 0; x < s.cols; x++ {
			i := y*s.cols + x
			c := canvas[i]
			if c.media != 0 || c.continuation || len(s.previous) == len(canvas) && c == s.previous[i] {
				continue
			}
			if x != lastX || y != lastY {
				fmt.Fprintf(&b, "\x1b[%d;%dH", y+1, x+1)
			}
			if c.fg != last.fg || c.bg != last.bg || c.style != last.style || lastX < 0 {
				fmt.Fprintf(&b, "\x1b[0;38;2;%d;%d;%d;48;2;%d;%d;%dm", c.fg>>16&255, c.fg>>8&255, c.fg&255, c.bg>>16&255, c.bg>>8&255, c.bg&255)
				for bit, code := range map[uint16]int{1: 1, 2: 2, 4: 3, 8: 4, 128: 9} {
					if c.style&bit != 0 {
						fmt.Fprintf(&b, "\x1b[%dm", code)
					}
				}
				last = c
			}
			b.WriteString(c.text)
			lastX, lastY = x+1, y
			if x+1 < s.cols && canvas[i+1].continuation {
				lastX = -1
			}
		}
	}
	if cursor.Width > 0 && cursor.Height > 0 && cursorX >= 0 && cursorX < s.cols && cursorY >= 0 && cursorY < s.rows {
		shape := cursor.Shape
		if shape < 1 || shape > 6 {
			shape = 2
		}
		fmt.Fprintf(&b, "\x1b[%d q\x1b[%d;%dH\x1b[?25h", shape, cursorY+1, cursorX+1)
	}
	b.WriteString("\x1b[0m\x1b[?2026l")
	if err := writeAll(out, []byte(b.String())); err != nil {
		return err
	}
	s.previous = canvas
	return nil
}

type inlineImage struct {
	media uint64
	rect  image.Rectangle
	page  int
	fit   string
}

func fitOption(fit string) string {
	switch fit {
	case "width":
		return "fit-width"
	case "height":
		return "fit-height"
	case "native":
		return ""
	default:
		return "fit-contain"
	}
}

func (s *screen) inline(surface *retainedScene, m api.MediaPlacement) (inlineImage, bool) {
	if m.ID == 0 || !validRect(m.X, m.Y, m.Width, m.Height) || !validRect(m.ClipX, m.ClipY, m.ClipWidth, m.ClipHeight) {
		return inlineImage{}, false
	}
	r := s.rect(surface.X+m.X, surface.Y+m.Y, m.Width, m.Height)
	return inlineImage{m.ID, r, max(1, m.Page), m.Fit}, !r.Empty()
}

func (s *screen) images() []inlineImage {
	var images []inlineImage
	seen := make(map[inlineImage]bool)
	for _, surface := range s.ordered() {
		for _, row := range surface.rows {
			for _, m := range row.Media {
				im, ok := s.inline(surface, m)
				if ok && !seen[im] {
					images = append(images, im)
					seen[im] = true
				}
			}
		}
	}
	sort.Slice(images, func(i, j int) bool {
		a, b := images[i], images[j]
		if a.rect.Min.Y != b.rect.Min.Y {
			return a.rect.Min.Y < b.rect.Min.Y
		}
		if a.rect.Min.X != b.rect.Min.X {
			return a.rect.Min.X < b.rect.Min.X
		}
		return a.media < b.media
	})
	return images
}

func writeAll(out io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := out.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func dcs(out io.Writer, command string) error {
	if strings.IndexFunc(command, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid control character in local display command")
	}
	return writeAll(out, []byte("\x1bP"+command+"\x1b\\"))
}
