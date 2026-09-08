//go:build !darwin && !linux

package main

import "errors"

func listProcessCwds() ([]string, error) {
	return nil, errors.New("process working directories are not readable on this platform")
}
