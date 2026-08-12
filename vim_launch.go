package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type vimLaunchSpec struct {
	Command []string
	Dir     string
	Title   string
}

// parseVimInvocation recognizes `st vim [vim-options] <file>`. The last
// argument is always the file; preceding arguments are forwarded to Vim.
func parseVimInvocation(args []string) (bool, []string, string, error) {
	if len(args) == 0 || args[0] != "vim" {
		return false, nil, "", nil
	}
	if len(args) < 2 {
		return true, nil, "", fmt.Errorf("st vim requires a file")
	}
	options := append([]string(nil), args[1:len(args)-1]...)
	if len(options) > 0 && options[len(options)-1] == "--" {
		options = options[:len(options)-1]
	}
	return true, options, args[len(args)-1], nil
}

func buildVimLaunch(options []string, file string) (vimLaunchSpec, error) {
	return buildVimLaunchWithLookPath(options, file, exec.LookPath)
}

func buildVimLaunchWithLookPath(options []string, file string, lookPath func(string) (string, error)) (vimLaunchSpec, error) {
	absFile, err := filepath.Abs(file)
	if err != nil {
		return vimLaunchSpec{}, err
	}
	absFile = filepath.Clean(absFile)
	dir := filepath.Dir(absFile)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return vimLaunchSpec{}, fmt.Errorf("vim working directory %q is not available", dir)
	}
	vimPath, err := lookPath("vim")
	if err != nil {
		return vimLaunchSpec{}, err
	}
	vimPath, err = filepath.Abs(vimPath)
	if err != nil {
		return vimLaunchSpec{}, err
	}
	args := append([]string{vimPath}, options...)
	args = append(args, "--", filepath.Base(absFile))
	return vimLaunchSpec{Command: args, Dir: dir, Title: "emulated-vim"}, nil
}

func emulatedVimRect(screenW, screenH, minW, minH, bottomReserve int) windowRect {
	w, h := screenW, screenH-bottomReserve
	if w < minW {
		w = minW
	}
	if h < minH {
		h = minH
	}
	if w > screenW {
		w = screenW
	}
	if h > screenH {
		h = screenH
	}
	return windowRect{x: 0, y: 0, w: w, h: h}
}
