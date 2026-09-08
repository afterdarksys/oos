package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/fleet"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestFleetCommand(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	p.Fleet = []string{"root@relay-b", "root@apps"}
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	old := fleet.SSH
	fleet.SSH = func(target string, timeout time.Duration, args ...string) ([]byte, error) {
		switch target {
		case "root@relay-b":
			return []byte(`{"version":"x","volume":"/","free_gb":187,"total_gb":295,"status":"OK","quarantine_bytes":0,"forecast":{}}`), nil
		case "root@apps":
			return []byte(`{"version":"x","volume":"/","free_gb":3,"total_gb":492,"status":"CRITICAL","quarantine_bytes":0,"forecast":{"rate_gb_per_hour":-2,"points":5,"window_hours":4,"free_gb":3}}`), nil
		case "dr1":
			return nil, errors.New("Connection timed out")
		}
		return nil, errors.New("unknown host")
	}
	t.Cleanup(func() { fleet.SSH = old })

	var out, errw bytes.Buffer
	code := doFleet(cfg, &opts{fleet: true}, &out, &errw)
	if code != status.ExitCritical {
		t.Errorf("worst host is critical: exit %d\n%s", code, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "2 of 2 hosts answered") || !strings.Contains(s, "critical") || !strings.Contains(s, "root@relay-b") {
		t.Errorf("table:\n%s", s)
	}
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if !strings.Contains(lines[2], "root@apps") {
		t.Errorf("least free first:\n%s", s)
	}

	// --hosts overrides the policy list; an unreachable host is exit 3
	out.Reset()
	code = doFleet(cfg, &opts{fleet: true, hosts: "dr1, root@relay-b", jsonOut: true}, &out, &errw)
	if code != status.ExitUsage {
		t.Errorf("unreachable host is exit %d, got %d", status.ExitUsage, code)
	}
	var j struct {
		Hosts []fleet.Host `json:"hosts"`
		Worst int          `json:"worst"`
	}
	if err := json.Unmarshal(out.Bytes(), &j); err != nil || len(j.Hosts) != 2 || j.Hosts[0].Target != "dr1" || j.Hosts[0].Err == "" || j.Worst != status.ExitUsage {
		t.Errorf("json: %v %+v", err, j)
	}

	// nothing to ask
	cfg.Policy.Fleet = nil
	if code := doFleet(cfg, &opts{fleet: true}, &out, &errw); code != status.ExitUsage || !strings.Contains(errw.String(), "no fleet") {
		t.Errorf("empty fleet must be a usage error: %d %s", code, errw.String())
	}
}
