//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// agentFiles returns a systemd user service and timer for an hourly quick check.
func agentFiles(home, exe string) map[string]string {
	dir := filepath.Join(home, ".config", "systemd", "user")
	service := fmt.Sprintf(`[Unit]
Description=oos disk headroom check

[Service]
Type=oneshot
ExecStart=%s --check --quick --notify
`, exe)
	timer := `[Unit]
Description=oos hourly disk headroom check

[Timer]
OnBootSec=5min
OnUnitActiveSec=1h
Persistent=true

[Install]
WantedBy=timers.target
`
	return map[string]string{
		filepath.Join(dir, "oos.service"): service,
		filepath.Join(dir, "oos.timer"):   timer,
	}
}

func agentInstall(home, exe string, run cmdRunner) error {
	for p, c := range agentFiles(home, exe) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			return err
		}
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	return run("systemctl", "--user", "enable", "--now", "oos.timer")
}

func agentUninstall(home string, run cmdRunner) error {
	_ = run("systemctl", "--user", "disable", "--now", "oos.timer")
	for p := range agentFiles(home, "oos") {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return run("systemctl", "--user", "daemon-reload")
}
