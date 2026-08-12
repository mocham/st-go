package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// listRarNames lists the entry names of a RAR/CBR archive using an installed
// tool (bsdtar then 7z). Only the directory is read — no file contents are
// extracted.
func listRarNames(archivePath string) ([]string, error) {
	if _, err := exec.LookPath("bsdtar"); err == nil {
		out, err := exec.Command("bsdtar", "-tf", archivePath).Output()
		if err == nil {
			return splitLines(string(out)), nil
		}
	}
	if _, err := exec.LookPath("7z"); err == nil {
		out, err := exec.Command("7z", "l", "-ba", archivePath).Output()
		if err == nil {
			// 7z -ba lines: "date  time  attr  size  comp  name..."
			var names []string
			for _, line := range strings.Split(string(out), "\n") {
				f := strings.Fields(line)
				if len(f) >= 6 {
					names = append(names, strings.Join(f[5:], " "))
				}
			}
			return names, nil
		}
	}
	return nil, fmt.Errorf("cannot list archive (no rar tool found)")
}

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSuffix(line, "/")
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// cleanEntry validates a virtual archive entry and returns its safe relative
// path inside an extraction directory.
func cleanEntry(entry string) (string, error) {
	name := filepath.Clean(entry)
	if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("unsafe archive entry %q", entry)
	}
	return name, nil
}

// extractZipEntry extracts a single entry from an open zip archive into
// destDir and returns the materialized path.
func extractZipEntry(zr *zip.ReadCloser, entry, destDir string) (string, error) {
	name, err := cleanEntry(entry)
	if err != nil {
		return "", err
	}
	for _, f := range zr.File {
		if f.Name != entry {
			continue
		}
		if f.FileInfo().IsDir() {
			return "", fmt.Errorf("%s is a directory", entry)
		}
		target := filepath.Join(destDir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return "", err
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			rc.Close()
			return "", err
		}
		_, cerr := io.Copy(out, rc)
		out.Close()
		rc.Close()
		if cerr != nil {
			os.Remove(target)
			return "", cerr
		}
		return target, nil
	}
	return "", fmt.Errorf("entry %q not found", entry)
}

// extractRarEntry extracts a single member from a RAR/CBR archive into
// destDir using an installed tool and returns the materialized path.
func extractRarEntry(archivePath, entry, destDir string) (string, error) {
	name, err := cleanEntry(entry)
	if err != nil {
		return "", err
	}
	target := filepath.Join(destDir, name)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", err
	}
	if _, err := exec.LookPath("7z"); err == nil {
		out, err := exec.Command("7z", "x", "-so", "-y", archivePath, entry).Output()
		if err == nil {
			if werr := os.WriteFile(target, out, 0644); werr != nil {
				return "", werr
			}
			return target, nil
		}
	}
	if _, err := exec.LookPath("bsdtar"); err == nil {
		if err := exec.Command("bsdtar", "-xf", archivePath, "-C", destDir, entry).Run(); err == nil {
			return target, nil
		}
	}
	return "", fmt.Errorf("cannot extract %q from %s", entry, archivePath)
}
