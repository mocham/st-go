package config

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
)

//go:embed config.json
var defaultConfigJSON []byte

// Config mirrors config.h of the C st, loaded from a .json file.
type Config struct {
	GeometryToken string `json:"-"`

	Font             string   `json:"font"`
	Borderpx         int      `json:"borderpx"`
	GlyphWidth       uint     `json:"glyphwidth"`
	GlyphHeight      uint     `json:"glyphheight"`
	GlyphBaseline    uint     `json:"glyphbaseline"`
	Shell            string   `json:"shell"`
	Utmp             string   `json:"utmp"`
	Scroll           string   `json:"scroll"`
	SttyArgs         string   `json:"stty_args"`
	Vtiden           string   `json:"vtiden"`
	WordDelimiters   string   `json:"worddelimiters"`
	AllowAltScreen   bool     `json:"allowaltscreen"`
	AllowWindowOps   bool     `json:"allowwindowops"`
	AllowGeometryOps bool     `json:"allowgeometryops"`
	Termname         string   `json:"termname"`
	Tabspaces        uint     `json:"tabspaces"`
	Colorname        []string `json:"colorname"`
	DefaultFg        uint     `json:"defaultfg"`
	DefaultBg        uint     `json:"defaultbg"`
	DefaultCs        uint     `json:"defaultcs"`
	DefaultRcs       uint     `json:"defaultrcs"`
	CursorShape      uint     `json:"cursorshape"`
	Cols             uint     `json:"cols"`
	Rows             uint     `json:"rows"`
	MouseShape       uint     `json:"mouseshape"`
	MouseFg          uint     `json:"mousefg"`
	MouseBg          uint     `json:"mousebg"`
	ForceMouseMod    uint     `json:"forcemousemod"`
	IgnoreMod        uint     `json:"ignoremod"`
	DoubleClickMs    uint     `json:"doubleclicktimeout"`
	TripleClickMs    uint     `json:"tripleclicktimeout"`
	MinLatency       float64  `json:"minlatency"`
	MaxLatency       float64  `json:"maxlatency"`
	BlinkTimeout     uint     `json:"blinktimeout"`
	CursorThick      uint     `json:"cursorthickness"`
	BellVolume       int      `json:"bellvolume"`
	WebPCachePath    string   `json:"webp_cache_path"`

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
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return c, err
	}
	return c, nil
}

// LoadResolved loads the config in this order:
//  1. an explicit -config path (when the flag was given);
//  2. config.json next to the executable;
//  3. the embedded config/config.json (via Default()).
//
// The embedded copy is the single source of truth for defaults, so a copied
// binary works standalone while still honoring an override placed beside it.
func LoadResolved(explicit string) (*Config, error) {
	if explicit != "" {
		return Load(explicit)
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "config.json")
		if _, err := os.Stat(cand); err == nil {
			return Load(cand)
		}
	}
	return Default(), nil
}
