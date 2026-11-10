//go:build !backend_web && !backend_fb

package main

import "st-go/platform_dependent/x11"

var openBackend = x11.Open
