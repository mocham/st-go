package main

import (
	_ "embed"

	"st-go/platform_dependent/x11"
	"st-go/platform_neutral/frontend"
)

//go:embed Monaco_Linux.ttf
var embeddedFont []byte

func main() { frontend.Main(x11.Open, embeddedFont) }
