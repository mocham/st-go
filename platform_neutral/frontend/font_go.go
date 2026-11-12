//go:build !backend_web && (!cgo || purego)

package frontend

import "st-go/platform_neutral/fontraster"

var openFontFace fontraster.Loader = fontraster.OpenGo
