//go:build !darwin && !linux

package guard

import "errors"

func OpenFilesByProcess() ([]OpenFile, error) {
	return nil, errors.New("open files by process: unsupported platform")
}
