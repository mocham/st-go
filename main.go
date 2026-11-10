package main

import (
	_ "embed"

	"st-go/platform_neutral/frontend"
)

//go:embed Monaco_Linux.ttf
var embeddedFont []byte

func main() { frontend.Main(openBackend, embeddedFont) }
