//go:build linux

package main

import (
	"os"
	"path/filepath"
)

// listOpenFiles reads /proc/<pid>/fd/* for every process we may inspect.
func listOpenFiles() ([]string, error) {
	fds, err := filepath.Glob("/proc/[0-9]*/fd/*")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, fd := range fds {
		if target, err := os.Readlink(fd); err == nil && len(target) > 0 && target[0] == '/' {
			files = append(files, target)
		}
	}
	return files, nil
}
