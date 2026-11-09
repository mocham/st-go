package frontend

import "st-go/platform_neutral/api"

type XGeometryMask = api.XGeometryMask

const (
	XValue      = api.XValue
	YValue      = api.YValue
	WidthValue  = api.WidthValue
	HeightValue = api.HeightValue
	AllValues   = api.AllValues
	XNegative   = api.XNegative
	YNegative   = api.YNegative
)

var parseGeometry = api.ParseGeometry
