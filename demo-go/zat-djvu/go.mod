module st-go/demo-go/zat-djvu

go 1.26.0

require (
	golang.org/x/sys v0.48.0
	golang.org/x/text v0.42.0
	st-go/limits v0.0.0
	st-go/pdfmeta v0.0.0
	st-go/ptyutil v0.0.0
	st-go/term v0.0.0
	st-go/tui v0.0.0
)

require (
	st-go/config v0.0.0 // indirect
	st-go/googlepinyin v0.0.0 // indirect
	st-go/platform_neutral/api v0.0.0 // indirect
	st-go/platform_neutral/stterminal v0.0.0 // indirect
)

replace (
	st-go/config => ../../functionalities/config
	st-go/googlepinyin => ../../functionalities/googlepinyin
	st-go/limits => ../../functionalities/limits
	st-go/pdfmeta => ../../functionalities/pdfmeta
	st-go/platform_neutral/api => ../../platform_neutral/api
	st-go/platform_neutral/stterminal => ../../platform_neutral/stterminal
	st-go/ptyutil => ../../functionalities/ptyutil
	st-go/term => ../../term
	st-go/tui => ../../functionalities/tui
)
