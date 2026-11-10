// Package term preserves the original import path during the relocation to
// platform_neutral/stterminal. The implementation and its tests live there.
package term

import (
	"st-go/config"
	"st-go/platform_neutral/stterminal"
)

type (
	Term                  = stterminal.Term
	Glyph                 = stterminal.Glyph
	Line                  = stterminal.Line
	TCursor               = stterminal.TCursor
	Selection             = stterminal.Selection
	CSIEscape             = stterminal.CSIEscape
	STREscape             = stterminal.STREscape
	Hooks                 = stterminal.Hooks
	CursorOverlayHooks    = stterminal.CursorOverlayHooks
	ImageDecodeOptions    = stterminal.ImageDecodeOptions
	GeometryUnit          = stterminal.GeometryUnit
	GeometryValue         = stterminal.GeometryValue
	WindowGeometryAction  = stterminal.WindowGeometryAction
	WindowGeometryRequest = stterminal.WindowGeometryRequest
	Region                = stterminal.Region
	RegionID              = stterminal.RegionID
	CellRect              = stterminal.CellRect
	DisplayRegion         = stterminal.DisplayRegion
	RegionImage           = stterminal.RegionImage
	RegionHooks           = stterminal.RegionHooks
	RegionNavigation      = stterminal.RegionNavigation
	RegionNavigationHooks = stterminal.RegionNavigationHooks
	RegionSearchHooks     = stterminal.RegionSearchHooks
	RegionDocumentQuery   = stterminal.RegionDocumentQuery
	RegionDocumentHooks   = stterminal.RegionDocumentHooks
	RegionList            = stterminal.RegionList
	RegionListItem        = stterminal.RegionListItem
	RegionListHooks       = stterminal.RegionListHooks
	PDFSearchRequest      = stterminal.PDFSearchRequest
	PDFSearchHooks        = stterminal.PDFSearchHooks
	ClipboardHooks        = stterminal.ClipboardHooks
)

const (
	ATTRNull                = stterminal.ATTRNull
	ATTRBold                = stterminal.ATTRBold
	ATTRFaint               = stterminal.ATTRFaint
	ATTRItalic              = stterminal.ATTRItalic
	ATTRUnderline           = stterminal.ATTRUnderline
	ATTRBlink               = stterminal.ATTRBlink
	ATTRReverse             = stterminal.ATTRReverse
	ATTRInvisible           = stterminal.ATTRInvisible
	ATTRStruck              = stterminal.ATTRStruck
	ATTRWrap                = stterminal.ATTRWrap
	ATTRWide                = stterminal.ATTRWide
	ATTRWdummy              = stterminal.ATTRWdummy
	ModeVisible             = stterminal.ModeVisible
	ModeFocused             = stterminal.ModeFocused
	ModeAppKeypad           = stterminal.ModeAppKeypad
	ModeMouseBtn            = stterminal.ModeMouseBtn
	ModeMouseMotion         = stterminal.ModeMouseMotion
	ModeReverse             = stterminal.ModeReverse
	ModeKbdLock             = stterminal.ModeKbdLock
	ModeHide                = stterminal.ModeHide
	ModeAppCursor           = stterminal.ModeAppCursor
	ModeMouseSgr            = stterminal.ModeMouseSgr
	Mode8bit                = stterminal.Mode8bit
	ModeBlink               = stterminal.ModeBlink
	ModeFBlink              = stterminal.ModeFBlink
	ModeFocus               = stterminal.ModeFocus
	ModeMouseX10            = stterminal.ModeMouseX10
	ModeMouseMany           = stterminal.ModeMouseMany
	ModeBrcktPaste          = stterminal.ModeBrcktPaste
	ModeNumLock             = stterminal.ModeNumLock
	ModeMouse               = stterminal.ModeMouse
	SelRegular              = stterminal.SelRegular
	SelRectangular          = stterminal.SelRectangular
	ImageRune               = stterminal.ImageRune
	GeometryPixels          = stterminal.GeometryPixels
	GeometryRatio           = stterminal.GeometryRatio
	GeometryRemember        = stterminal.GeometryRemember
	GeometryRestore         = stterminal.GeometryRestore
	GeometryForget          = stterminal.GeometryForget
	GeometryPlace           = stterminal.GeometryPlace
	MainRegion              = stterminal.MainRegion
	SecondaryRegion         = stterminal.SecondaryRegion
	RegionViewerZoomMaximum = stterminal.RegionViewerZoomMaximum
)

func NewTerm(cfg *config.Config, hooks Hooks) *Term { return stterminal.NewTerm(cfg, hooks) }
func TrueColor(r, g, b uint) uint32                 { return stterminal.TrueColor(r, g, b) }
func IsTrueColor(value uint32) bool                 { return stterminal.IsTrueColor(value) }
func CellWidth(r rune) int                          { return stterminal.CellWidth(r) }
