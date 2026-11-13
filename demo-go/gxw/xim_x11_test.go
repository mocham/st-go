package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestX11XIMWithXlib(t *testing.T) {
	if os.Getenv("GXW_TEST_X11") != "1" {
		t.Skip("run make test-gxw-x11")
	}
	assets := filepath.Join("..", "..", "TEST-ASSETS", "x11", "bin")
	if err := os.MkdirAll(assets, 0755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(assets, "xim-client")
	if output, err := exec.Command("cc", "-O2", "-o", binary, "testdata/xim-client.c", "-lX11", "-lXtst").CombinedOutput(); err != nil {
		t.Fatalf("Xlib test client: %v\n%s", err, output)
	}
	display := testDisplay(t)
	w, err := newWM(options{display: display, fontSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.run(ctx) }()
	defer func() { cancel(); <-done }()
	clientCtx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	cmd := exec.CommandContext(clientCtx, binary)
	cmd.Env = append(os.Environ(), "DISPLAY="+display, "XMODIFIERS=@im=gxw", "LC_ALL=C.UTF-8")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Xlib IME round trip: %v\n%s", err, output)
	}
	t.Logf("Xlib committed: %s", output)
}
