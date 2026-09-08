//go:build darwin

package guard

import (
	"os/exec"
	"strings"
)

// ListOpenFiles returns the path of every open file of every process lsof
// can see. lsof exits non-zero when some processes are off limits but still
// prints the rest, so non-empty output is the answer.
func ListOpenFiles() ([]string, error) {
	out, err := exec.Command("lsof", "-F", "n", "-w", "-n", "-P").Output()
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			files = append(files, line[1:])
		}
	}
	return files, nil
}
