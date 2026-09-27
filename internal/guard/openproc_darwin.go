//go:build darwin

package guard

import (
	"strconv"
	"strings"
)

// OpenFilesByProcess lists every open regular path with the process that
// holds it: lsof -F pcn prints p<pid>, c<command>, then n<path> per file.
// Paths are unescaped so they can be stat'd.
func OpenFilesByProcess() ([]OpenFile, error) {
	var files []OpenFile
	pid, cmd := 0, ""
	err := runLines("lsof", []string{"-b", "-w", "-F", "pcn", "-n", "-P"}, true, func(line string) {
		if len(line) < 2 {
			return
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
			cmd = ""
		case 'c':
			cmd = decodeLsofName(line[1:])
		case 'n':
			if strings.HasPrefix(line, "n/") && !strings.HasPrefix(line, "n/dev/") {
				files = append(files, OpenFile{PID: pid, Command: cmd, Path: decodeLsofName(line[1:])})
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
