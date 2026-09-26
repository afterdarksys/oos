//go:build darwin

package agent

import "os/exec"

// notify posts a macOS user notification through osascript. Title and
// message travel as argv to an "on run" handler, never spliced into the
// script source, so no quote or backslash in them can change the script.
func notify(title, msg string) error {
	return exec.Command("osascript", notifyArgs(title, msg)...).Run()
}

func notifyArgs(title, msg string) []string {
	return []string{
		"-e", "on run argv",
		"-e", "display notification (item 1 of argv) with title (item 2 of argv)",
		"-e", "end run",
		"--", msg, title,
	}
}
