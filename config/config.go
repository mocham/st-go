package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"st-go/limits"
)

//go:embed config.json
var defaultConfigJSON []byte

// Config mirrors config.h of the C st, loaded from a .json file.
type Config struct {
	GeometryToken    string `json:"-"`
	NativeVideoToken string `json:"-"`

	Font                string   `json:"font"`
	Borderpx            int      `json:"borderpx"`
	GlyphWidth          uint     `json:"glyphwidth"`
	GlyphHeight         uint     `json:"glyphheight"`
	GlyphBaseline       uint     `json:"glyphbaseline"`
	Shell               string   `json:"shell"`
	Utmp                string   `json:"utmp"`
	Scroll              string   `json:"scroll"`
	SttyArgs            string   `json:"stty_args"`
	Vtiden              string   `json:"vtiden"`
	WordDelimiters      string   `json:"worddelimiters"`
	AllowAltScreen      bool     `json:"allowaltscreen"`
	AllowWindowOps      bool     `json:"allowwindowops"`
	AllowClipboardWrite bool     `json:"allowclipboardwrite"`
	AllowClipboardRead  bool     `json:"allowclipboardread"`
	AllowGeometryOps    bool     `json:"allowgeometryops"`
	Termname            string   `json:"termname"`
	Tabspaces           uint     `json:"tabspaces"`
	Colorname           []string `json:"colorname"`
	DefaultFg           uint     `json:"defaultfg"`
	DefaultBg           uint     `json:"defaultbg"`
	DefaultCs           uint     `json:"defaultcs"`
	DefaultRcs          uint     `json:"defaultrcs"`
	CursorShape         uint     `json:"cursorshape"`
	Cols                uint     `json:"cols"`
	Rows                uint     `json:"rows"`
	MouseShape          uint     `json:"mouseshape"`
	MouseFg             uint     `json:"mousefg"`
	MouseBg             uint     `json:"mousebg"`
	ForceMouseMod       uint     `json:"forcemousemod"`
	IgnoreMod           uint     `json:"ignoremod"`
	DoubleClickMs       uint     `json:"doubleclicktimeout"`
	TripleClickMs       uint     `json:"tripleclicktimeout"`
	MinLatency          float64  `json:"minlatency"`
	MaxLatency          float64  `json:"maxlatency"`
	BlinkTimeout        uint     `json:"blinktimeout"`
	CursorThick         uint     `json:"cursorthickness"`
	BellVolume          int      `json:"bellvolume"`
	WebPCachePath       string   `json:"webp_cache_path"`

	FileBrowser FileBrowserConfig `json:"file_browser"`

	Mshortcuts []Mshortcut `json:"mshortcuts"`
	Shortcuts  []Shortcut  `json:"shortcuts"`
	Keymap     []Keydef    `json:"keymap"`
	Selmasks   [3]uint     `json:"selmasks"`
}

type FileBrowserConfig struct {
	Icons FileBrowserIcons `json:"icons"`
}

type FileBrowserIcons struct {
	Parent     string `json:"parent"`
	Directory  string `json:"directory"`
	Symlink    string `json:"symlink"`
	Image      string `json:"image"`
	PDF        string `json:"pdf"`
	Text       string `json:"text"`
	Archive    string `json:"archive"`
	Audio      string `json:"audio"`
	Video      string `json:"video"`
	Code       string `json:"code"`
	Config     string `json:"config"`
	Executable string `json:"executable"`
	Default    string `json:"default"`
}

type Mshortcut struct {
	Mask    uint   `json:"mask"`
	Button  uint   `json:"button"`
	Action  string `json:"action"`
	Arg     string `json:"arg"`
	Release bool   `json:"release"`
}

type Shortcut struct {
	Mask   uint   `json:"mask"`
	Keysym string `json:"keysym"`
	Action string `json:"action"`
	Arg    string `json:"arg"`
}

// Keydef: appkey >0 when keypad app mode enabled, <0 disabled, 0 no value.
// Appcursor likewise for cursor application mode.
type Keydef struct {
	Keysym string `json:"keysym"`
	Mask   int    `json:"mask"`
	Str    string `json:"str"`
	Appkey int    `json:"appkey"`
	Appcur int    `json:"appcursor"`
}

// Default returns the st defaults (the embedded config.json, matching the C
// st's compiled-in config.h). This is the single source of truth for the
// default shortcuts/mshortcuts/keymap etc., so the binary works standalone
// even when no external config file is present.
func Default() *Config {
	c := &Config{}
	if err := json.Unmarshal(defaultConfigJSON, c); err != nil {
		panic("config: embedded config.json is invalid: " + err.Error())
	}
	return c
}

func Load(path string) (*Config, error) {
	c := Default()
	// A stale FIFO/device beside a relocated executable must not stall startup.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return c, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return c, err
	}
	if !info.Mode().IsRegular() || info.Size() > limits.ControlBytes {
		return c, fmt.Errorf("config must be a regular file of at most %d bytes", limits.ControlBytes)
	}
	b, err := io.ReadAll(io.LimitReader(f, limits.ControlBytes+1))
	if err != nil {
		return c, err
	}
	if len(b) > limits.ControlBytes {
		return c, fmt.Errorf("config exceeds %d bytes", limits.ControlBytes)
	}
	if err := json.Unmarshal(b, c); err != nil {
		return Default(), err // Unmarshal may have already applied some fields.
	}
	if err := c.validate(); err != nil {
		return Default(), err
	}
	// Relative paths belong to the configuration, not the launch directory.
	// Bare font names retain the font loader's normal name lookup.
	if c.Font != "" && !filepath.IsAbs(c.Font) {
		candidate := filepath.Join(filepath.Dir(path), c.Font)
		if strings.ContainsAny(c.Font, `/\`) {
			c.Font, _ = filepath.Abs(candidate)
		} else if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			c.Font, _ = filepath.Abs(candidate)
		}
	}
	return c, nil
}

// Reject settings that would make startup fail or produce an unreadable window.
// Callers always receive an intact embedded configuration on failure.
func (c *Config) validate() error {
	if c.Cols == 0 || c.Rows == 0 || c.Cols > 65535 || c.Rows > 65535 ||
		c.GlyphWidth == 0 || c.GlyphHeight == 0 || c.GlyphWidth > 65535 || c.GlyphHeight > 65535 ||
		c.Borderpx < 0 || c.Borderpx > 65535 {
		return fmt.Errorf("invalid terminal or glyph dimensions")
	}
	w := uint64(c.Cols)*uint64(c.GlyphWidth) + uint64(c.Borderpx)*2
	h := uint64(c.Rows)*uint64(c.GlyphHeight) + uint64(c.Borderpx)*2
	if w > 65535 || h > 65535 || w*h > limits.PixelBytes/4 ||
		uint64(c.Cols)*uint64(c.Rows) > limits.MaxBytes/40 ||
		uint64(c.GlyphHeight)*uint64(c.GlyphHeight) > limits.PixelBytes/4 {
		return fmt.Errorf("configured window exceeds resource limits")
	}
	if c.GlyphBaseline == 0 || c.GlyphBaseline > c.GlyphHeight {
		return fmt.Errorf("glyph baseline must be within the cell")
	}
	if c.Tabspaces == 0 || c.Tabspaces > limits.MaxBytes {
		return fmt.Errorf("invalid tab spacing")
	}
	if c.MinLatency < 0 || c.MaxLatency < 0 || c.MinLatency > 1000 || c.MaxLatency > 1000 {
		return fmt.Errorf("paint latency must be between 0 and 1000 milliseconds")
	}
	if c.BlinkTimeout > 60000 || c.CursorShape > 7 || c.CursorThick == 0 || c.CursorThick > c.GlyphHeight {
		return fmt.Errorf("invalid cursor or blink settings")
	}
	// The frontend always uses slots 256..259 for cursor/foreground/background.
	// A 16-color array alone previously made the missing text colors both black.
	if len(c.Colorname) < 20 {
		return fmt.Errorf("colorname must include 16 ANSI colors and 4 default colors")
	}
	for _, color := range c.Colorname {
		s := strings.TrimPrefix(color, "#")
		if _, err := strconv.ParseUint(s, 16, 24); len(s) != 6 || err != nil {
			return fmt.Errorf("invalid palette color %q", color)
		}
	}
	for _, index := range []uint{c.DefaultFg, c.DefaultBg, c.DefaultCs, c.DefaultRcs} {
		if index >= uint(len(c.Colorname)+240) {
			return fmt.Errorf("default color index %d is outside the palette", index)
		}
	}
	if c.DefaultFg == c.DefaultBg || strings.EqualFold(c.Colorname[18], c.Colorname[19]) {
		return fmt.Errorf("default foreground and background must differ")
	}
	return nil
}

// LoadResolved loads the config in this order:
//  1. an explicit -config path (when the flag was given);
//  2. st.json next to the executable (also written by make install);
//  3. legacy config.json next to the executable;
//  4. the embedded config/config.json (via Default()).
//
// The embedded copy is the single source of truth for defaults, so a copied
// binary works standalone while still honoring an override placed beside it.
func LoadResolved(explicit string) (*Config, error) {
	exe, err := os.Executable()
	if err != nil && explicit == "" {
		return Default(), nil
	}
	return loadResolved(explicit, filepath.Dir(exe))
}

func loadResolved(explicit, dir string) (*Config, error) {
	if explicit != "" {
		return Load(explicit)
	}
	for _, name := range []string{"st.json", "config.json"} {
		candidate := filepath.Join(dir, name)
		if _, err := os.Lstat(candidate); !os.IsNotExist(err) {
			return Load(candidate)
		}
	}
	return Default(), nil
}
