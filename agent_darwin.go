//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const agentLabel = "com.afterdarksys.oos"

// agentSystemFiles: --system is a Linux idea; launchd agents are per user.
func agentSystemFiles(exe string) map[string]string { return nil }

// agentFiles returns the launchd plist that runs an hourly agent tick.
func agentFiles(home, exe string) map[string]string {
	plist := filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist")
	logDir := filepath.Join(home, ".local", "state", "oos")
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>--agent-tick</string>
  </array>
  <key>StartInterval</key><integer>3600</integer>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>%s/agent.log</string>
  <key>StandardErrorPath</key><string>%s/agent.log</string>
</dict>
</plist>
`, agentLabel, exe, logDir, logDir)
	return map[string]string{plist: content}
}

func agentInstall(home, exe string, system bool, run cmdRunner) error {
	if system {
		return errors.New("--system is for Linux servers; on macOS the per-user LaunchAgent is the supported form")
	}
	files := agentFiles(home, exe)
	if err := os.MkdirAll(filepath.Join(home, ".local", "state", "oos"), 0o755); err != nil {
		return err
	}
	var plist string
	for p, c := range files {
		plist = p
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			return err
		}
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = run("launchctl", "bootout", domain, plist) // not loaded yet is fine
	return run("launchctl", "bootstrap", domain, plist)
}

func agentUninstall(home string, system bool, run cmdRunner) error {
	plist := filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist")
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = run("launchctl", "bootout", domain, plist)
	if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
