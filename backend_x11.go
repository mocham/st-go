//go:build !backend_web

package main

import "st-go/platform_dependent/x11"

var openBackend = x11.Open
