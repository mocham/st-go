//go:build linux

package fb

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type mouseInfo struct {
	name                         string
	relKnown, relative, absolute bool
}

func readMouseInfo(path string) mouseInfo {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return readMouseInfoAt(filepath.Join("/sys/class/input", filepath.Base(path), "device"))
}

func readMouseInfoAt(base string) mouseInfo {
	var info mouseInfo
	if name, err := os.ReadFile(filepath.Join(base, "name")); err == nil {
		info.name = strings.TrimSpace(string(name))
	}
	if data, err := os.ReadFile(filepath.Join(base, "capabilities/rel")); err == nil {
		// REL_MAX fits in one word on every kernel architecture. Sysfs prints
		// native kernel words, which may be wider than this process's words.
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if bits, err := strconv.ParseUint(fields[len(fields)-1], 16, 64); err == nil {
				info.relKnown = true
				info.relative = bits&3 == 3
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(base, "capabilities/abs")); err == nil {
		for _, word := range strings.Fields(string(data)) {
			if bits, err := strconv.ParseUint(word, 16, 64); err == nil && bits != 0 {
				info.absolute = true
				break
			}
		}
	}
	return info
}

func mouseDiagnosticPaths() ([]string, error) {
	paths, err := filepath.Glob("/dev/input/event*")
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool)
	for _, path := range paths {
		known[path] = true
	}
	// A container may expose host input metadata but omit the device nodes.
	// Include those paths so the diagnostic names the missing mouse, rather
	// than incorrectly claiming that there is no mouse on the machine.
	sysfs, err := filepath.Glob("/sys/class/input/event*")
	if err != nil {
		return nil, err
	}
	for _, path := range sysfs {
		device := filepath.Join("/dev/input", filepath.Base(path))
		if !known[device] {
			paths = append(paths, device)
			known[device] = true
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func writeMouseList(w io.Writer) error {
	paths, err := mouseDiagnosticPaths()
	if err != nil {
		return err
	}
	return describeMice(w, paths, readMouseInfo, openMouse)
}

func describeMice(w io.Writer, paths []string, infoFor func(string) mouseInfo, open func(string) (*mouse, error)) error {
	if _, err := fmt.Fprintln(w, "Mouse device diagnostic (read-only; no framebuffer, VT changes, grabs or event reads):"); err != nil {
		return err
	}
	available := 0
	for _, path := range paths {
		info := infoFor(path)
		kind := "other/unknown input device"
		if info.relative {
			kind = "relative pointer"
		} else if info.absolute {
			kind = "absolute-only device (not yet supported)"
		}
		status := ""
		m, err := open(path)
		if err != nil {
			status = err.Error()
		} else {
			available++
			status = fmt.Sprintf("OK: relative mouse; high-resolution wheel=%t horizontal=%t", m.hiWheel, m.hiHWheel)
			m.close()
		}
		// Device names and errors may contain user/device supplied control bytes.
		if _, err := fmt.Fprintf(w, "%s  name=%q  type=%s\n  %q\n", path, info.name, kind, status); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "Accessible supported mice: %d\n", available)
	if err == nil && available == 0 {
		_, err = fmt.Fprintln(w, "Check that the host mouse event node exists and is readable by the user running st-fb. Framebuffer/keyboard access alone does not grant mouse access.")
	}
	return err
}
