//go:build backend_fb

package main

import (
	"flag"

	"st-go/platform_dependent/fb"
	"st-go/platform_neutral/api"
)

var framebufferOptions fb.Options

func init() {
	framebufferOptions.RegisterFlags(flag.CommandLine)
	backendStartup.FontScale = framebufferOptions.FontScale
}

func openBackend(window api.WindowOptions) (api.Host, error) {
	return fb.New(window, framebufferOptions)
}
