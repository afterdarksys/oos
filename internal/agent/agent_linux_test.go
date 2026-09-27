//go:build linux

package agent

import (
	"strings"
	"testing"
)

func TestSystemUnitsTolerateMissingPathsAndSpreadTicks(t *testing.T) {
	var service, timer string
	for p, c := range SystemFiles("/usr/local/bin/oos") {
		switch {
		case strings.HasSuffix(p, "oos.service"):
			service = c
		case strings.HasSuffix(p, "oos.timer"):
			timer = c
		}
	}
	for _, want := range []string{"ProtectSystem=strict\n", "ReadWritePaths=-/root -/var -/tmp -/home\n"} {
		if !strings.Contains(service, want) {
			t.Errorf("service lacks %q:\n%s", want, service)
		}
	}
	for _, line := range strings.Split(service, "\n") {
		if !strings.HasPrefix(line, "ReadWritePaths=") {
			continue
		}
		for _, p := range strings.Fields(strings.TrimPrefix(line, "ReadWritePaths=")) {
			if !strings.HasPrefix(p, "-") {
				t.Errorf("%s without '-' fails the unit with 226/NAMESPACE where it is missing", p)
			}
		}
	}
	for _, want := range []string{"OnCalendar=hourly\n", "Persistent=true\n", "RandomizedDelaySec=15m\n", "AccuracySec=1m\n"} {
		if !strings.Contains(timer, want) {
			t.Errorf("timer lacks %q:\n%s", want, timer)
		}
	}
	// Persistent= only applies to OnCalendar; mixing in monotonic
	// triggers would double the ticks after a boot.
	for _, bad := range []string{"OnUnitActiveSec", "OnBootSec"} {
		if strings.Contains(timer, bad) {
			t.Errorf("timer carries %s:\n%s", bad, timer)
		}
	}
	for p, c := range Files("/home/u", "/usr/local/bin/oos") {
		if strings.HasSuffix(p, ".service") && strings.Contains(c, "ProtectSystem") {
			t.Error("user units carry no system hardening block")
		}
	}
}
