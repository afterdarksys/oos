//go:build !linux

package guard

import (
	"os"
	"strconv"
	"strings"
)

// ListProcesses returns every other process's command line, untruncated (-ww:
// without it macOS ps clips long lines and a path deep in an argument list is
// silently missed). Our own is dropped: an oos invocation names the paths it
// is judging, and must never count as a process that uses them. ps runs under
// CommandTimeout; a hang or failure is an error, never a shorter list.
func ListProcesses() ([]string, error) {
	self := strconv.Itoa(os.Getpid())
	var procs []string
	err := runLines("ps", []string{"-axww", "-o", "pid=,command="}, false, func(line string) {
		pid, cmd, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || pid == self {
			return
		}
		procs = append(procs, strings.TrimSpace(cmd))
	})
	if err != nil {
		return nil, err
	}
	return procs, nil
}
