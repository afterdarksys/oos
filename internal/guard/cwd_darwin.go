//go:build darwin

package guard

import (
	"os/exec"
	"strings"
)

// ListProcessCwds asks lsof for every process's working directory. The -F n
// form prints one "n<path>" line per cwd.
func ListProcessCwds() ([]string, error) {
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-F", "n", "-w", "-n", "-P").Output()
	if err != nil {
		// lsof exits 1 when some processes could not be inspected but still
		// prints the rest; treat output as the answer when there is any.
		if len(out) == 0 {
			return nil, err
		}
	}
	var cwds []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			cwds = append(cwds, line[1:])
		}
	}
	return cwds, nil
}
