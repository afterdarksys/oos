//go:build !darwin && !linux

package agent

import "errors"

func Files(home, exe string) map[string]string { return nil }

func SystemFiles(exe string) map[string]string { return nil }

func Install(home, exe string, system bool, run Runner) error {
	return errors.New("scheduled agent is not supported on this platform")
}

func Uninstall(home string, system bool, run Runner) error {
	return errors.New("scheduled agent is not supported on this platform")
}
