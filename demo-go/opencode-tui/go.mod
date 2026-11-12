module st-go/demo-go/opencode-tui

go 1.26.0

require (
	golang.org/x/sys v0.49.0
	modernc.org/sqlite v1.62.0
	st-go/limits v0.0.0
	st-go/platform_neutral/api v0.0.0
	st-go/tui v0.0.0
)

require (
	codeberg.org/go-fonts/liberation v0.6.0 // indirect
	codeberg.org/go-pdf/fpdf v0.12.0 // indirect
	github.com/d2lang/mathjax-go v0.1.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mocham/GoLS/latexpdf v0.0.0 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	st-go/googlepinyin v0.0.0 // indirect
)

replace (
	github.com/mocham/GoLS/latexpdf => ../../functionalities/latexpdf
	st-go/googlepinyin => ../../functionalities/googlepinyin
	st-go/limits => ../../functionalities/limits
	st-go/platform_neutral/api => ../../platform_neutral/api
	st-go/tui => ../../functionalities/tui
)
