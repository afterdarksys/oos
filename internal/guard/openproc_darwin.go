//go:build darwin

package guard

import (
	"os/exec"
	"strconv"
	"strings"
)

// OpenFilesByProcess lists every open regular path with the process that
// holds it: lsof -F pcn prints p<pid>, c<command>, then n<path> per file.
// lsof exits non-zero when some processes are off limits but still prints
// the rest, so non-empty output is the answer.
func OpenFilesByProcess() ([]OpenFile, error) {
	out, err := exec.Command("lsof", "-F", "pcn", "-w", "-n", "-P").Output()
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var files []OpenFile
	pid, cmd := 0, ""
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
			cmd = ""
		case 'c':
			cmd = line[1:]
		case 'n':
			if strings.HasPrefix(line, "n/") && !strings.HasPrefix(line, "n/dev/") {
				files = append(files, OpenFile{PID: pid, Command: cmd, Path: line[1:]})
			}
		}
	}
	return files, nil
}
