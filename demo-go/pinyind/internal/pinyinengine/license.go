package pinyinengine

import _ "embed"

//go:embed assets/LICENSE
var license string

//go:embed assets/NOTICE
var notice string

func License() string { return notice + "\n" + license }
