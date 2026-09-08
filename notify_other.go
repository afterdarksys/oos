//go:build !darwin && !linux

package main

import "errors"

func notify(title, msg string) error {
	return errors.New("notifications are not supported on this platform")
}
