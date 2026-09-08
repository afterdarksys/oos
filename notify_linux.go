//go:build linux

package main

import "os/exec"

// notify posts a desktop notification through notify-send (libnotify).
func notify(title, msg string) error {
	return exec.Command("notify-send", "--app-name=oos", title, msg).Run()
}
