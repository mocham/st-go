//go:build backend_web

package main

import (
	"flag"

	"st-go/platform_dependent/web"
)

var openBackend = web.RegisterFlags(flag.CommandLine)
