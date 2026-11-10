module st-go/tui

go 1.26.0

require (
	github.com/mocham/GoLS/latexpdf v0.0.0
	golang.org/x/sys v0.48.0
	golang.org/x/text v0.42.0
	st-go/limits v0.0.0
	st-go/platform_neutral/api v0.0.0
)

require (
	codeberg.org/go-fonts/liberation v0.6.0 // indirect
	codeberg.org/go-pdf/fpdf v0.12.0 // indirect
	github.com/d2lang/mathjax-go v0.1.0 // indirect
	golang.org/x/net v0.60.0 // indirect
)

replace (
	github.com/mocham/GoLS/latexpdf => ../latexpdf
	st-go/limits => ../limits
	st-go/platform_neutral/api => ../../platform_neutral/api
)
