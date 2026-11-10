//go:build backend_fb

package main

import (
	"flag"

	"st-go/platform_dependent/fb"
)

var openBackend = fb.RegisterFlags(flag.CommandLine)
