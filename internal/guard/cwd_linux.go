//go:build linux

package guard

import (
	"os"
	"path/filepath"
)

// ListProcessCwds reads /proc/<pid>/cwd for every process. It fails with
// ErrPIDNamespace when other processes may be outside its view, and with
// *UnreadableProcessesError when any live process cannot be read: a process
// owned by another user can still be working inside this user's files.
func ListProcessCwds() ([]string, error) {
	var cwds []string
	err := walkProcs("working directories", true, func(_ int, dir string) error {
		target, err := os.Readlink(filepath.Join(dir, "cwd"))
		if err != nil {
			return err
		}
		cwds = append(cwds, target)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cwds, nil
}
