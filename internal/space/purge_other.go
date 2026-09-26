//go:build !darwin

package space

import "errors"

// Purgeable is a macOS CacheDelete figure. Other systems have nothing to ask.
func Purgeable(string) (int64, error) {
	return 0, errors.New("purgeable space is reported on macOS")
}
