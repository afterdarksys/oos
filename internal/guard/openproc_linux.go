//go:build linux

package guard

import (
	"os"
	"path/filepath"
	"strings"
)

// OpenFilesByProcess reads /proc/<pid>/fd/* with /proc/<pid>/comm for every
// process we may inspect. It only names writers for the daemon's report, so
// unreadable processes are skipped; a missing /proc is still an error.
func OpenFilesByProcess() ([]OpenFile, error) {
	pids, err := procPIDs()
	if err != nil {
		return nil, err
	}
	var files []OpenFile
	for _, pid := range pids {
		dir := procDir(pid)
		fds, err := readFDs(dir)
		if err != nil {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join(dir, "comm"))
		cmd := strings.TrimSpace(string(comm))
		for _, target := range fds {
			if strings.HasPrefix(target, "/dev/") || strings.HasPrefix(target, "/proc/") {
				continue
			}
			files = append(files, OpenFile{PID: pid, Command: cmd, Path: target})
		}
	}
	return files, nil
}
