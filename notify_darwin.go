//go:build darwin

package main

import (
	"os/exec"
	"strings"
)

// notify posts a macOS user notification through osascript.
func notify(title, msg string) error {
	esc := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
	script := `display notification "` + esc(msg) + `" with title "` + esc(title) + `"`
	return exec.Command("osascript", "-e", script).Run()
}
