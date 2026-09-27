//go:build darwin

package guard

import "strings"

// ListProcessCwds asks lsof for every process's working directory. The -F n
// form prints one "n<path>" line per cwd; -b keeps lsof off calls that block
// on a dead network mount and -w drops warnings so stderr means failure.
func ListProcessCwds() ([]string, error) {
	var cwds []string
	err := runLines("lsof", []string{"-b", "-w", "-a", "-d", "cwd", "-F", "n", "-n", "-P"}, true, func(line string) {
		if strings.HasPrefix(line, "n/") {
			cwds = append(cwds, line[1:])
		}
	})
	if err != nil {
		return nil, err
	}
	return cwds, nil
}
