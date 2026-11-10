module st-go

go 1.26.0

require (
	github.com/BurntSushi/xgb v0.0.0-20210121224620-deaf085860bc
	github.com/BurntSushi/xgbutil v0.0.0-20190907113008-ad855c713046
	golang.org/x/sys v0.48.0
)

require github.com/hajimehoshi/go-mp3 v0.3.4

require github.com/mattn/go-sqlite3 v1.14.32

require (
	github.com/alecthomas/chroma/v2 v2.27.0
	github.com/dlclark/regexp2/v2 v2.2.1 // indirect
)

require (
	codeberg.org/go-fonts/liberation v0.6.0
	codeberg.org/go-pdf/fpdf v0.12.0 // indirect
	github.com/d2lang/mathjax-go v0.1.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

require (
	github.com/mocham/GoLS/latexpdf v0.0.0
	golang.org/x/net v0.60.0
)

replace github.com/mocham/GoLS/latexpdf => ./latexpdf
