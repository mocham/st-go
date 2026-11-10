module st-go/widgets

go 1.26.0

require (
	st-go/limits v0.0.0
	st-go/platform_neutral/api v0.0.0
)

replace (
	st-go/limits => ../limits
	st-go/platform_neutral/api => ../../platform_neutral/api
)
