//go:build linux

package guard

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// OpenFilesByProcess reads /proc/<pid>/fd/* with /proc/<pid>/comm for every
// process we may inspect.
func OpenFilesByProcess() ([]OpenFile, error) {
	pids, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return nil, err
	}
	var files []OpenFile
	for _, dir := range pids {
		pid, err := strconv.Atoi(filepath.Base(dir))
		if err != nil {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join(dir, "comm"))
		cmd := strings.TrimSpace(string(comm))
		fds, err := os.ReadDir(filepath.Join(dir, "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "/dev/") || strings.HasPrefix(target, "/proc/") {
				continue
			}
			files = append(files, OpenFile{PID: pid, Command: cmd, Path: target})
		}
	}
	return files, nil
}
