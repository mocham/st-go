module st-go/demo-go/file-browser

go 1.26.0

require (
	github.com/hajimehoshi/go-mp3 v0.3.4
	github.com/mattn/go-sqlite3 v1.14.32
	github.com/mocham/GoLS/latexpdf v0.0.0
	golang.org/x/net v0.60.0
	golang.org/x/sys v0.48.0
	st-go/config v0.0.0
	st-go/limits v0.0.0
	st-go/platform_neutral/api v0.0.0
	st-go/ptyutil v0.0.0
	st-go/semhtml v0.0.0
	st-go/term v0.0.0
	st-go/tui v0.0.0
)

require (
	codeberg.org/go-fonts/liberation v0.6.0 // indirect
	codeberg.org/go-pdf/fpdf v0.12.0 // indirect
	github.com/d2lang/mathjax-go v0.1.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	st-go/googlepinyin v0.0.0 // indirect
	st-go/pdfmeta v0.0.0 // indirect
	st-go/platform_neutral/stterminal v0.0.0 // indirect
)

replace (
	github.com/mocham/GoLS/latexpdf => ../../functionalities/latexpdf
	st-go/config => ../../functionalities/config
	st-go/googlepinyin => ../../functionalities/googlepinyin
	st-go/limits => ../../functionalities/limits
	st-go/pdfmeta => ../../functionalities/pdfmeta
	st-go/platform_neutral/api => ../../platform_neutral/api
	st-go/platform_neutral/stterminal => ../../platform_neutral/stterminal
	st-go/ptyutil => ../../functionalities/ptyutil
	st-go/semhtml => ../../functionalities/semhtml
	st-go/term => ../../term
	st-go/tui => ../../functionalities/tui
)
