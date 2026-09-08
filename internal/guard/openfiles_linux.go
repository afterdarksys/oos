//go:build linux

package guard

import (
	"os"
	"path/filepath"
)

// ListOpenFiles reads /proc/<pid>/fd/* for every process we may inspect.
func ListOpenFiles() ([]string, error) {
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
