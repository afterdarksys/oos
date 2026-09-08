package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/fleet"
	"github.com/afterdarksys/oos/internal/status"
)

// doFleet asks every target for its quick check and prints one table. The
// targets come from --hosts, else policy.fleet. Exit code is the worst host:
// 3 when one could not be reached, else the worst disk status.
func doFleet(cfg *config.Config, o *opts, out, errw io.Writer) int {
	var targets []string
	if strings.TrimSpace(o.hosts) != "" {
		for _, h := range strings.Split(o.hosts, ",") {
			if h = strings.TrimSpace(h); h != "" {
				targets = append(targets, h)
			}
		}
	} else {
		targets = cfg.Policy.Fleet
	}
	if len(targets) == 0 {
		fmt.Fprintln(errw, "oos: no fleet: set policy.fleet in the config or pass --hosts a,b,c")
		return status.ExitUsage
	}
	timeout := fleet.DefaultTimeout
	if cfg.Policy.FleetTimeoutSeconds > 0 {
		timeout = time.Duration(cfg.Policy.FleetTimeoutSeconds) * time.Second
	}
	start := time.Now()
	hosts := fleet.Collect(targets, timeout)
	code := fleet.Worst(hosts)
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"hosts": hosts, "worst": code, "elapsed_ms": time.Since(start).Milliseconds()})
		return code
	}
	reach := 0
	for _, h := range hosts {
		if h.Err == "" {
			reach++
		}
	}
	fmt.Fprintf(out, "fleet: %d of %d hosts answered in %s\n", reach, len(hosts), time.Since(start).Round(100*time.Millisecond))
	fleet.Report(out, hosts, o.verbose)
	return code
}
