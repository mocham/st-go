module st-go

go 1.26.0

require (
	github.com/BurntSushi/xgb v0.0.0-20210121224620-deaf085860bc
	github.com/BurntSushi/xgbutil v0.0.0-20190907113008-ad855c713046
	golang.org/x/sys v0.49.0
)

require (
	codeberg.org/go-fonts/liberation v0.6.0
	golang.org/x/text v0.42.0
)

require (
	golang.org/x/image v0.40.0
	golang.org/x/net v0.60.0
	modernc.org/sqlite v1.62.0
	st-go/audio v0.0.0
	st-go/config v0.0.0
	st-go/limits v0.0.0
	st-go/pdfmeta v0.0.0
	st-go/platform_neutral/api v0.0.0
	st-go/platform_neutral/stterminal v0.0.0
	st-go/ptyutil v0.0.0
	st-go/term v0.0.0
	st-go/terminfo v0.0.0
	st-go/tui v0.0.0
	st-go/widgets v0.0.0
)

require (
	codeberg.org/go-pdf/fpdf v0.12.0 // indirect
	github.com/d2lang/mathjax-go v0.1.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hajimehoshi/go-mp3 v0.3.4 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mocham/GoLS/latexpdf v0.0.0 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace (
	github.com/mocham/GoLS/latexpdf => ./functionalities/latexpdf
	st-go/audio => ./functionalities/audio
	st-go/config => ./functionalities/config
	st-go/googlepinyin => ./functionalities/googlepinyin
	st-go/limits => ./functionalities/limits
	st-go/pdfmeta => ./functionalities/pdfmeta
	st-go/platform_neutral/api => ./platform_neutral/api
	st-go/platform_neutral/stterminal => ./platform_neutral/stterminal
	st-go/ptyutil => ./functionalities/ptyutil
	st-go/semhtml => ./functionalities/semhtml
	st-go/term => ./term
	st-go/terminfo => ./functionalities/terminfo
	st-go/tui => ./functionalities/tui
	st-go/widgets => ./functionalities/widgets
)
