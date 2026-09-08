//go:build linux

package guard

import (
	"os"
	"path/filepath"
)

// ListProcessCwds reads /proc/<pid>/cwd for every process we may inspect.
// Processes owned by other users are unreadable and skipped; that is fine
// because they cannot be using this user's cache directories.
func ListProcessCwds() ([]string, error) {
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
