//go:build linux

package daemon

import (
	"strings"
	"testing"

	"github.com/afterdarksys/oos/internal/agent"
)

func TestDaemonUnitQuotesAndStopsOnPermanentFailure(t *testing.T) {
	exe := `/home/a b/R&D <x> "q" 100%/oos`
	for _, c := range Files(t.TempDir(), exe, false) {
		for _, want := range []string{
			"ExecStart=" + agent.SystemdQuote(exe) + " --daemon\n",
			"Restart=on-failure\n",
			"StartLimitIntervalSec=600\n",
			"StartLimitBurst=5\n",
			"IOSchedulingClass=idle\n",
		} {
			if !strings.Contains(c, want) {
				t.Errorf("unit lacks %q:\n%s", want, c)
			}
		}
		if strings.Contains(c, "Restart=always") {
			t.Error("Restart=always loops on a permanent error")
		}
	}
}

func TestDaemonSystemUnitToleratesMissingPaths(t *testing.T) {
	for _, c := range Files(t.TempDir(), "/usr/local/bin/oos", true) {
		if !strings.Contains(c, "ProtectSystem=strict\n") || !strings.Contains(c, "ReadWritePaths=-/root -/var -/tmp -/home\n") {
			t.Errorf("system unit must keep ProtectSystem=strict with '-' prefixed paths:\n%s", c)
		}
	}
}
