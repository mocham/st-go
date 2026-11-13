package main

import (
	_ "embed"

	"st-go/platform_neutral/frontend"
)

//go:embed Monaco_Linux.ttf
var embeddedFont []byte

var backendStartup frontend.StartupOptions

func main() { frontend.MainWithOptions(openBackend, embeddedFont, backendStartup) }
