module st-go/demo-go/st-client

go 1.26.0

require (
	golang.org/x/net v0.60.0
	golang.org/x/sys v0.48.0
	st-go/config v0.0.0
	st-go/platform_neutral/api v0.0.0
	st-go/platform_neutral/stterminal v0.0.0
	st-go/semhtml v0.0.0
)

require (
	codeberg.org/go-fonts/liberation v0.6.0 // indirect
	codeberg.org/go-pdf/fpdf v0.12.0 // indirect
	github.com/d2lang/mathjax-go v0.1.0 // indirect
	github.com/mocham/GoLS/latexpdf v0.0.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	st-go/limits v0.0.0 // indirect
	st-go/pdfmeta v0.0.0 // indirect
)

replace (
	github.com/mocham/GoLS/latexpdf => ../../functionalities/latexpdf
	st-go/config => ../../functionalities/config
	st-go/limits => ../../functionalities/limits
	st-go/pdfmeta => ../../functionalities/pdfmeta
	st-go/platform_neutral/api => ../../platform_neutral/api
	st-go/platform_neutral/stterminal => ../../platform_neutral/stterminal
	st-go/semhtml => ../../functionalities/semhtml
)
