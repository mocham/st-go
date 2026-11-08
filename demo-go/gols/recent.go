package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxRecentDatabases = 20

type recentDatabaseFile struct {
	Databases []string `json:"databases"`
}

func recentDatabasesPath() (string, error) {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	if home == "" {
		return "", fmt.Errorf("home directory is unavailable")
	}
	return filepath.Join(home, ".cache", "gols.json"), nil
}

func loadRecentDatabases() ([]string, error) {
	filePath, err := recentDatabasesPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state recentDatabaseFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filePath, err)
	}
	return normalizeRecentDatabases(state.Databases), nil
}

func recordRecentDatabases(opened []string) error {
	opened = normalizeRecentDatabases(opened)
	if len(opened) == 0 {
		return nil
	}
	existing, err := loadRecentDatabases()
	if err != nil {
		// This is a disposable cache. A malformed old file should not prevent a
		// successfully opened database from becoming the new recent list.
		existing = nil
	}
	merged := normalizeRecentDatabases(append(opened, existing...))
	filePath, err := recentDatabasesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(filePath), ".gols-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(recentDatabaseFile{Databases: merged}); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, filePath); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

func normalizeRecentDatabases(paths []string) []string {
	result := make([]string, 0, min(len(paths), maxRecentDatabases))
	seen := make(map[string]bool, len(paths))
	for _, filePath := range paths {
		filePath = strings.TrimSpace(filePath)
		if filePath == "" {
			continue
		}
		absolute, err := filepath.Abs(filePath)
		if err != nil {
			continue
		}
		if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
			absolute = resolved
		}
		absolute = filepath.Clean(absolute)
		if seen[absolute] {
			continue
		}
		seen[absolute] = true
		result = append(result, absolute)
		if len(result) == maxRecentDatabases {
			break
		}
	}
	return result
}
