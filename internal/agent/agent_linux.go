//go:build linux

package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// Files returns a systemd user service and timer for an hourly tick.
func Files(home, exe string) map[string]string {
	return agentUnits(filepath.Join(home, ".config", "systemd", "user"), exe, false)
}

// SystemFiles returns the root units --install-agent --system writes.
func SystemFiles(exe string) map[string]string {
	return agentUnits("/etc/systemd/system", exe, true)
}

// agentUnits renders the service and timer into dir. System units run as
// root and carry a hardening block; user units rely on the user's own scope.
func agentUnits(dir, exe string, system bool) map[string]string {
	hardening := ""
	if system {
		hardening = `User=root
ProtectSystem=strict
ReadWritePaths=/root /var /tmp /home
PrivateTmp=no
NoNewPrivileges=yes
`
	}
	service := fmt.Sprintf(`[Unit]
Description=oos disk headroom check

[Service]
Type=oneshot
ExecStart=%s --agent-tick
%s`, exe, hardening)
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

func systemctlArgs(system bool, args ...string) []string {
	if system {
		return args
	}
	return append([]string{"--user"}, args...)
}

func Install(home, exe string, system bool, run Runner) error {
	dir := filepath.Join(home, ".config", "systemd", "user")
	if system {
		dir = "/etc/systemd/system"
	}
	for p, c := range agentUnits(dir, exe, system) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			return err
		}
	}
	if err := run("systemctl", systemctlArgs(system, "daemon-reload")...); err != nil {
		return err
	}
	return run("systemctl", systemctlArgs(system, "enable", "--now", "oos.timer")...)
}

func Uninstall(home string, system bool, run Runner) error {
	dir := filepath.Join(home, ".config", "systemd", "user")
	if system {
		dir = "/etc/systemd/system"
	}
	_ = run("systemctl", systemctlArgs(system, "disable", "--now", "oos.timer")...)
	for p := range agentUnits(dir, "oos", system) {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return run("systemctl", systemctlArgs(system, "daemon-reload")...)
}
