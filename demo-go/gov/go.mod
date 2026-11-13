module st-go/demo-go/gov

go 1.26.0

require (
	golang.org/x/sys v0.48.0
	st-go/platform_neutral/api v0.0.0
	st-go/tui v0.0.0
)

replace st-go/platform_neutral/api => ../../platform_neutral/api

replace st-go/tui => ../../functionalities/tui
