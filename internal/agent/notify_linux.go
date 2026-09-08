//go:build linux

package agent

import (
	"errors"
	"os/exec"
)

// notify posts a desktop notification through notify-send (libnotify). On a
// headless host there is no desktop: the message goes to syslog through
// logger instead, where the journal and any log shipper pick it up. Only when
// neither exists is it an error.
func notify(title, msg string) error {
	if p, err := exec.LookPath("notify-send"); err == nil {
		return exec.Command(p, "--app-name=oos", title, msg).Run()
	}
	if p, err := exec.LookPath("logger"); err == nil {
		return exec.Command(p, "-t", "oos", "-p", "user.warning", title+": "+msg).Run()
	}
	return errors.New("no notify-send or logger on this host")
}
