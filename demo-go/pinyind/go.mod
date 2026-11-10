module st-go/demo-go/pinyind

go 1.26.0

require (
	golang.org/x/sys v0.48.0
	st-go/tui v0.0.0
)

replace (
	github.com/mocham/GoLS/latexpdf => ../../functionalities/latexpdf
	st-go/limits => ../../functionalities/limits
	st-go/platform_neutral/api => ../../platform_neutral/api
	st-go/tui => ../../functionalities/tui
)
