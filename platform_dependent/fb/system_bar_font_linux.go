//go:build linux

package fb

import "st-go/widgets/systembar"

//go:generate go run generate_system_bar_font.go -font ../../Monaco_Linux.ttf -output ../../functionalities/widgets/systembar/font_generated.go

const (
	systemBarFontSize         = systembar.FontSize
	systemBarFontCellWidth    = systembar.CellWidth
	systemBarFontCellHeight   = systembar.CellHeight
	systemBarFontAlphabet     = systembar.FontAlphabet
	systemBarFontSourceSHA256 = systembar.FontSourceSHA256
)

var systemBarGlyphs = systembar.NewStyle(1).Glyphs
var systemBarIcons = func() map[byte][]uint32 {
	icons := make(map[byte][]uint32)
	for kind := systembar.BatteryIcon; kind <= systembar.NewTerminalIcon; kind++ {
		icons[kind] = systemBarGlyphs[kind]
	}
	return icons
}()
var systemBarBlend = systembar.Blend

type systemBarStyle struct {
	cellWidth, cellHeight int
	padding, focusWidth   int
	glyphs                [256][]uint32
}

func newSystemBarStyle(scale float64) *systemBarStyle {
	s := systembar.NewStyle(scale)
	return &systemBarStyle{cellWidth: s.CellWidth, cellHeight: s.CellHeight, padding: s.Padding, focusWidth: s.FocusWidth, glyphs: s.Glyphs}
}
