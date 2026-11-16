module st-go/demo-go/gov2

go 1.26.0

require (
	golang.org/x/image v0.40.0
	golang.org/x/sys v0.49.0
	st-go/audio v0.0.0
	st-go/platform_neutral/api v0.0.0
	st-go/ptyutil v0.0.0
)

require github.com/hajimehoshi/go-mp3 v0.3.4 // indirect

replace st-go/audio => ../../functionalities/audio

replace st-go/platform_neutral/api => ../../platform_neutral/api

replace st-go/ptyutil => ../../functionalities/ptyutil
