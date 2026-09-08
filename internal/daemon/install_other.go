//go:build !darwin && !linux

package daemon

import (
	"errors"

	"github.com/afterdarksys/oos/internal/agent"
)

func Files(home, exe string, system bool) map[string]string { return nil }

func Install(home, exe string, system bool, run agent.Runner) error {
	return errors.New("daemon install: unsupported platform")
}

func Uninstall(home string, system bool, run agent.Runner) error {
	return errors.New("daemon uninstall: unsupported platform")
}
