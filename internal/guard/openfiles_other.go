//go:build !darwin && !linux

package guard

import "errors"

func ListOpenFiles() ([]string, error) {
	return nil, errors.New("open files are not readable on this platform")
}
