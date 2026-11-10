package main

import (
	"os"
	"strings"
)

func browserBackend() string {
	switch backend := strings.ToLower(os.Getenv("ST_GO_BACKEND")); backend {
	case "x11", "fb", "web":
		return backend
	default:
		return "unknown"
	}
}

func validOpenPlatform(platform string) bool {
	switch platform {
	case "x11", "fb", "web", "all":
		return true
	}
	return false
}

func openPlatformMatches(platform string) bool {
	return platform == "all" || platform == browserBackend()
}

func registeredOpenCommand() (string, bool) {
	backend := browserBackend()
	if backend != "unknown" {
		if command, ok := os.LookupEnv("ST_FILE_BROWSER_OPEN_" + strings.ToUpper(backend)); ok {
			return command, true
		}
	}
	if command, ok := os.LookupEnv("ST_FILE_BROWSER_OPEN_ALL"); ok {
		return command, true
	}
	if backend == "x11" {
		return os.LookupEnv("ST_FILE_BROWSER_OPEN")
	}
	return "", false
}
