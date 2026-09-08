//go:build !darwin && !linux

package guard

import "errors"

func ListProcessCwds() ([]string, error) {
	return nil, errors.New("process working directories are not readable on this platform")
}
