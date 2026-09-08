//go:build linux

package main

import (
	"os"
	"path/filepath"
)

// listProcessCwds reads /proc/<pid>/cwd for every process we may inspect.
// Processes owned by other users are unreadable and skipped; that is fine
// because they cannot be using this user's cache directories.
func listProcessCwds() ([]string, error) {
	links, err := filepath.Glob("/proc/[0-9]*/cwd")
	if err != nil {
		return nil, err
	}
	var cwds []string
	for _, l := range links {
		if target, err := os.Readlink(l); err == nil {
			cwds = append(cwds, target)
		}
	}
	return cwds, nil
}
