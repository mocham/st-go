package config

import (
	"encoding/json"
	"os"
)

// Config mirrors config.h of the C st, loaded from a .json file.
type Config struct {
	Font           string   `json:"font"`
	Borderpx       int      `json:"borderpx"`
	GlyphWidth     uint     `json:"glyphwidth"`
	GlyphHeight    uint     `json:"glyphheight"`
	GlyphBaseline  uint     `json:"glyphbaseline"`
	Shell          string   `json:"shell"`
	Utmp           string   `json:"utmp"`
	Scroll         string   `json:"scroll"`
	SttyArgs       string   `json:"stty_args"`
	Vtiden         string   `json:"vtiden"`
	WordDelimiters string   `json:"worddelimiters"`
	AllowAltScreen bool     `json:"allowaltscreen"`
	AllowWindowOps bool     `json:"allowwindowops"`
	Termname       string   `json:"termname"`
	Tabspaces      uint     `json:"tabspaces"`
	Colorname      []string `json:"colorname"`
	DefaultFg      uint     `json:"defaultfg"`
	DefaultBg      uint     `json:"defaultbg"`
	DefaultCs      uint     `json:"defaultcs"`
	DefaultRcs     uint     `json:"defaultrcs"`
	CursorShape    uint     `json:"cursorshape"`
	Cols           uint     `json:"cols"`
	Rows           uint     `json:"rows"`
	MouseShape     uint     `json:"mouseshape"`
	MouseFg        uint     `json:"mousefg"`
	MouseBg        uint     `json:"mousebg"`
	ForceMouseMod  uint     `json:"forcemousemod"`
	IgnoreMod      uint     `json:"ignoremod"`
	DoubleClickMs  uint     `json:"doubleclicktimeout"`
	TripleClickMs  uint     `json:"tripleclicktimeout"`
	MinLatency     float64  `json:"minlatency"`
	MaxLatency     float64  `json:"maxlatency"`
	BlinkTimeout   uint     `json:"blinktimeout"`
	CursorThick    uint     `json:"cursorthickness"`
	BellVolume     int      `json:"bellvolume"`

	Mshortcuts []Mshortcut `json:"mshortcuts"`
	Shortcuts  []Shortcut  `json:"shortcuts"`
	Keymap     []Keydef    `json:"keymap"`
	Selmasks   [3]uint     `json:"selmasks"`
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
	Keysym   string `json:"keysym"`
	Mask     int    `json:"mask"`
	Str      string `json:"str"`
	Appkey   int    `json:"appkey"`
	Appcur   int    `json:"appcursor"`
}

// Default returns the st defaults when no file is given.
func Default() *Config {
	return &Config{
		Font:           "Monaco_Linux.ttf",
		Borderpx:       1,
		GlyphWidth:     16,
		GlyphHeight:    28,
		GlyphBaseline:  24,
		Shell:          "/bin/sh",
		SttyArgs:       "stty raw pass8 nl -echo -iexten -cstopb 38400",
		Vtiden:         "\x1b[?6c",
		WordDelimiters: " ",
		AllowAltScreen: true,
		AllowWindowOps: false,
		Termname:       "st-256color",
		Tabspaces:      8,
		Colorname: []string{
			"#181818", "#ac4242", "#90a959", "#f4bf75",
			"#6a9fb5", "#aa759f", "#75b5aa", "#d8d8d8",
			"#6b6b6b", "#c55555", "#aac474", "#feca88",
			"#82b8c8", "#c28cb8", "#93d3c3", "#f8f8f8",
			"#cccccc", "#555555", "#add8e6", "#181818",
		},
		DefaultFg:      258,
		DefaultBg:      259,
		DefaultCs:      256,
		DefaultRcs:     257,
		CursorShape:    2,
		Cols:           80,
		Rows:           24,
		MouseFg:        7,
		MouseBg:        0,
		ForceMouseMod:  1 << 0, // ShiftMask
		IgnoreMod:      1 << 1, // Mod2Mask (numlock)
		DoubleClickMs:  300,
		TripleClickMs:  600,
		MinLatency:     2,
		MaxLatency:     33,
		BlinkTimeout:   800,
		CursorThick:    2,
		BellVolume:     0,
	}
}

func Load(path string) (*Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return c, err
	}
	return c, nil
}
