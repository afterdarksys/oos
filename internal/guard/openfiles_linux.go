//go:build linux

package guard

// ListOpenFiles reads /proc/<pid>/fd/* for every process, failing like
// ListProcessCwds when the answer could be incomplete.
func ListOpenFiles() ([]string, error) {
	var files []string
	err := walkProcs("open files", true, func(_ int, dir string) error {
		fds, err := readFDs(dir)
		if err != nil {
			return err
		}
		files = append(files, fds...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
