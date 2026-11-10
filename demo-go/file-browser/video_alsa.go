package main

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed alsa.conf
var alsaConfData []byte

var alsaConfPath string

// Video configuration is data for the native-video host, not a sound-device
// connection. Prefer the supplied/system config without loading libasound.
func videoALSAConfig(cacheDir string) (string, error) {
	if path := os.Getenv("ALSA_CONFIG_PATH"); path != "" {
		return filepath.Abs(path)
	}
	for _, directory := range []string{os.Getenv("ALSA_CONFIG_DIR"), "/usr/share/alsa", "/usr/local/share/alsa"} {
		if directory == "" {
			continue
		}
		path := filepath.Join(directory, "alsa.conf")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return filepath.Abs(path)
		}
	}
	if err := writeALSAConfig(cacheDir); err != nil {
		return "", err
	}
	return alsaConfPath, nil
}

// Only the separate native-video helper materializes this fallback. Browser
// startup and MP3 playback never change the terminal's inherited ALSA config.
func writeALSAConfig(cacheDir string) error {
	path := filepath.Join(cacheDir, "alsa.conf")
	if err := os.WriteFile(path, alsaConfData, 0644); err != nil {
		return err
	}
	alsaConfPath = path
	return os.Setenv("ALSA_CONFIG_PATH", path)
}
