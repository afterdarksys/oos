//go:build !darwin && !linux

package main

import "errors"

func listOpenFiles() ([]string, error) {
	return nil, errors.New("open files are not readable on this platform")
}
