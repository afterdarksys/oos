//go:build darwin

package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/afterdarksys/oos/internal/agent"
)

const label = "com.afterdarksys.oos.daemon"

// Files returns the LaunchAgent that keeps the daemon running.
func Files(home, exe string, system bool) map[string]string {
	plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	logDir := filepath.Join(home, ".local", "state", "oos")
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>--daemon</string>
  </array>
  <key>KeepAlive</key><true/>
  <key>RunAtLoad</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>%s/daemon.log</string>
  <key>StandardErrorPath</key><string>%s/daemon.log</string>
</dict>
</plist>
`, label, exe, logDir, logDir)
	return map[string]string{plist: content}
}

// Install writes the plist and bootstraps it. --system is a Linux idea.
func Install(home, exe string, system bool, run agent.Runner) error {
	if system {
		return errors.New("--system is for Linux servers; on macOS the per-user LaunchAgent is the supported form")
	}
	if err := os.MkdirAll(filepath.Join(home, ".local", "state", "oos"), 0o755); err != nil {
		return err
	}
	var plist string
	for p, c := range Files(home, exe, false) {
		plist = p
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			return err
		}
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = run("launchctl", "bootout", domain, plist)
	return run("launchctl", "bootstrap", domain, plist)
}

func Uninstall(home string, system bool, run agent.Runner) error {
	plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	_ = run("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), plist)
	if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
