module st-go/demo-go/gxw

go 1.26.0

require (
	github.com/golang/freetype v0.0.0-20170609003504-e2365dfdc4a0
	github.com/jezek/xgb v1.3.1
	golang.org/x/image v0.40.0
	st-go/audio v0.0.0
	st-go/brightness v0.0.0
	st-go/config v0.0.0
	st-go/limits v0.0.0
	st-go/tui v0.0.0
	st-go/widgets v0.0.0
)

require (
	github.com/hajimehoshi/go-mp3 v0.3.4 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	st-go/googlepinyin v0.0.0 // indirect
)

replace (
	github.com/mocham/GoLS/latexpdf => ../../functionalities/latexpdf
	st-go/audio => ../../functionalities/audio
	st-go/brightness => ../../functionalities/brightness
	st-go/config => ../../functionalities/config
	st-go/googlepinyin => ../../functionalities/googlepinyin
	st-go/limits => ../../functionalities/limits
	st-go/platform_neutral/api => ../../platform_neutral/api
	st-go/tui => ../../functionalities/tui
	st-go/widgets => ../../functionalities/widgets
)
