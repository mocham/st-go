module st-go

go 1.26.0

require (
	github.com/BurntSushi/xgb v0.0.0-20210121224620-deaf085860bc
	github.com/BurntSushi/xgbutil v0.0.0-20190907113008-ad855c713046
	golang.org/x/sys v0.48.0
)

require github.com/hajimehoshi/go-mp3 v0.3.4

require (
	github.com/d2lang/mathjax-go v0.1.0
	github.com/mattn/go-sqlite3 v1.14.32
	github.com/srwiley/oksvg v0.0.0-20221011165216-be6e8873101c
	github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef
	golang.org/x/image v0.40.0
)

require (
	codeberg.org/go-fonts/liberation v0.6.0 // indirect
	codeberg.org/go-pdf/fpdf v0.12.0 // indirect
)

require (
	github.com/mocham/GoLS/latexpdf v0.0.0
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

replace github.com/mocham/GoLS/latexpdf => ../AA/lecture_server/latexpdf
