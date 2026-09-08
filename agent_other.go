//go:build !darwin && !linux

package main

import "errors"

func agentFiles(home, exe string) map[string]string { return nil }

func agentInstall(home, exe string, system bool, run cmdRunner) error {
	return errors.New("scheduled agent is not supported on this platform")
}

func agentUninstall(home string, system bool, run cmdRunner) error {
	return errors.New("scheduled agent is not supported on this platform")
}
