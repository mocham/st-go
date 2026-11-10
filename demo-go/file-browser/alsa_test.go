package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteALSAConfig(t *testing.T) {
	cache := t.TempDir()
	if err := writeALSAConfig(cache); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cache, "alsa.conf")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, alsaConfData) {
		t.Fatal("embedded alsa.conf was not written verbatim")
	}
	if got := os.Getenv("ALSA_CONFIG_PATH"); got != path {
		t.Fatalf("ALSA_CONFIG_PATH = %q, want %q", got, path)
	}
	if !bytes.Contains(data, []byte("defaults.pcm")) {
		t.Fatal("embedded alsa.conf is missing the defaults section")
	}
}

func TestBrowserSetupPreservesWorkingALSAConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provided-alsa.conf")
	data := []byte("# supplied working config\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, config := range []string{"", path} {
		t.Run(config, func(t *testing.T) {
			t.Setenv("ALSA_CONFIG_PATH", config)
			b := &Browser{}
			b.setup()
			if b.cacheDir == "" {
				t.Fatal("browser cache directory was not created")
			}
			t.Cleanup(func() { os.RemoveAll(b.cacheDir) })
			if os.Getenv("ALSA_CONFIG_PATH") != config {
				t.Fatal("browser replaced the MP3 player's ALSA config")
			}
			if _, err := os.Stat(filepath.Join(b.cacheDir, "alsa.conf")); !os.IsNotExist(err) {
				t.Fatal("MP3 startup materialized an unexpected ALSA config")
			}
		})
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("supplied ALSA config was overwritten", err)
	}
}

func TestVideoRespectsProvidedALSAConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "given.conf")
	t.Setenv("ALSA_CONFIG_PATH", path)
	got, err := videoALSAConfig(t.TempDir())
	if err != nil || got != path || os.Getenv("ALSA_CONFIG_PATH") != path {
		t.Fatal("video replaced the MP3 configuration", got, err)
	}
}
