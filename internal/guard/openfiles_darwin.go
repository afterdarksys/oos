//go:build darwin

package guard

import "strings"

// ListOpenFiles returns the path of every open file of every process lsof
// can see. Names are kept as lsof prints them (Referenced also tries the
// unescaped form). Exit status 1 without stderr is "nothing more to report";
// a timeout or any reported error fails the listing.
func ListOpenFiles() ([]string, error) {
	var files []string
	err := runLines("lsof", []string{"-b", "-w", "-F", "n", "-n", "-P"}, true, func(line string) {
		if strings.HasPrefix(line, "n/") {
			files = append(files, line[1:])
		}
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
