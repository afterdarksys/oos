//go:build linux

package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/afterdarksys/oos/internal/agent"
)

const unit = "oos-daemon.service"

func unitDir(home string, system bool) string {
	if system {
		return "/etc/systemd/system"
	}
	return filepath.Join(home, ".config", "systemd", "user")
}

// Files returns the systemd service that keeps the daemon running. It is
// restarted only after a failure, and gives up after five failures in ten
// minutes so a permanent error does not loop forever. ReadWritePaths
// entries carry "-" so a host without /home does not fail the unit with
// 226/NAMESPACE (see agent.agentUnits).
func Files(home, exe string, system bool) map[string]string {
	hardening := ""
	if system {
		hardening = `User=root
ProtectSystem=strict
ReadWritePaths=-/root -/var -/tmp -/home
PrivateTmp=no
NoNewPrivileges=yes
`
	}
	service := fmt.Sprintf(`[Unit]
Description=oos disk watcher daemon
After=network.target
StartLimitIntervalSec=600
StartLimitBurst=5

[Service]
Type=simple
ExecStart=%s --daemon
Restart=on-failure
RestartSec=10
Nice=10
IOSchedulingClass=idle
%s
[Install]
WantedBy=default.target
`, agent.SystemdQuote(exe), hardening)
	return map[string]string{filepath.Join(unitDir(home, system), unit): service}
}

func args(system bool, a ...string) []string {
	if system {
		return a
	}
	return append([]string{"--user"}, a...)
}

func Install(home, exe string, system bool, run agent.Runner) error {
	for p, c := range Files(home, exe, system) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			return err
		}
	}
	if err := run("systemctl", args(system, "daemon-reload")...); err != nil {
		return err
	}
	return run("systemctl", args(system, "enable", "--now", unit)...)
}

func Uninstall(home string, system bool, run agent.Runner) error {
	_ = run("systemctl", args(system, "disable", "--now", unit)...)
	if err := os.Remove(filepath.Join(unitDir(home, system), unit)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return run("systemctl", args(system, "daemon-reload")...)
}
