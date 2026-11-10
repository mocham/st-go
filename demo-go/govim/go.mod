module st-go/demo-go/govim

go 1.26.0

require (
	github.com/alecthomas/chroma/v2 v2.27.0
	golang.org/x/sys v0.48.0
	golang.org/x/text v0.42.0
	st-go/config v0.0.0
	st-go/limits v0.0.0
	st-go/ptyutil v0.0.0
	st-go/term v0.0.0
	st-go/tui v0.0.0
)

require (
	codeberg.org/go-fonts/liberation v0.6.0 // indirect
	codeberg.org/go-pdf/fpdf v0.12.0 // indirect
	github.com/d2lang/mathjax-go v0.1.0 // indirect
	github.com/dlclark/regexp2/v2 v2.2.1 // indirect
	github.com/mocham/GoLS/latexpdf v0.0.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	st-go/pdfmeta v0.0.0 // indirect
	st-go/platform_neutral/api v0.0.0 // indirect
	st-go/platform_neutral/stterminal v0.0.0 // indirect
)

replace (
	github.com/mocham/GoLS/latexpdf => ../../functionalities/latexpdf
	st-go/config => ../../functionalities/config
	st-go/limits => ../../functionalities/limits
	st-go/pdfmeta => ../../functionalities/pdfmeta
	st-go/platform_neutral/api => ../../platform_neutral/api
	st-go/platform_neutral/stterminal => ../../platform_neutral/stterminal
	st-go/ptyutil => ../../functionalities/ptyutil
	st-go/term => ../../term
	st-go/tui => ../../functionalities/tui
)
