package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/leftovers"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/status"
)

// doLeftovers pairs every entry in the ~/Library areas apps write to with an
// installed app; orphans (bundle id with no app) come first with --add
// lines, unmatched names next, then what is installed (-v). --min-mb floor
// (default 10); --tag orphan|unmatched|installed|known narrows.
func doLeftovers(cfg *config.Config, env guard.Env, o *opts, now time.Time, out, errw io.Writer) int {
	f, err := filterFromOpts(o)
	if err != nil {
		fmt.Fprintln(errw, "oos:", err)
		return status.ExitUsage
	}
	minBytes := int64(10 << 20)
	if o.minMB > 0 {
		minBytes = o.minMB << 20
	}
	apps := leftovers.InstalledApps(env.Home)
	res, err := leftovers.Scan(cfg, env.Home, apps, minBytes, f, now)
	if err != nil {
		fmt.Fprintf(errw, "oos: app-leftovers: %v\n", err)
		return status.ExitUsage
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"apps_seen": res.Apps, "rows": res.Rows, "bytes_by_verdict": res.ByVerdict, "elapsed_ms": res.Elapsed.Milliseconds()})
		return status.ExitOK
	}
	fmt.Fprintf(out, "app leftovers under ~/Library (%d apps seen, entries over %s, %s%s):\n", res.Apps, size.Human(minBytes), res.Elapsed.Round(100*time.Millisecond), f.Describe())
	var lines []string
	for _, r := range res.Rows {
		if line := leftovers.AddLine(r, env.Home); line != "" {
			lines = append(lines, line)
		}
		for _, h := range r.Safe {
			if line := leftovers.SafeAddLine(h, env.Home); line != "" {
				lines = append(lines, line)
			}
		}
	}
	shown := 0
	for _, r := range res.Rows {
		// Installed apps stay quiet unless -v, except when a keep folder
		// contains a cache worth showing on its own.
		if r.Verdict == "installed" && !o.verbose && len(r.Safe) == 0 {
			continue
		}
		if f.Top > 0 && shown >= f.Top {
			break
		}
		shown++
		app := r.App
		if r.Reason != "" {
			if app != "" {
				app += " (" + r.Reason + ")"
			} else {
				app = r.Reason
			}
		}
		fmt.Fprintf(out, "  %9s  %4dd  %-10s %-12s %-26s %-36s %s\n", size.Human(r.Bytes), int(now.Sub(r.ModTime).Hours()/24), r.Verdict, r.Safety, r.Area, r.Name, app)
		for _, h := range r.Safe {
			fmt.Fprintf(out, "             safe  %9s  %s  %s\n", size.Human(h.Bytes), h.Path, h.Reason)
		}
		if o.verbose {
			for _, h := range r.Kept {
				fmt.Fprintf(out, "             keep  %9s  %s  %s\n", size.Human(h.Bytes), h.Path, h.Reason)
			}
		}
	}
	fmt.Fprintf(out, "by verdict: orphan %s, unmatched %s, known %s, installed %s (-v lists installed)\n",
		size.Human(res.ByVerdict["orphan"]), size.Human(res.ByVerdict["unmatched"]), size.Human(res.ByVerdict["known"]), size.Human(res.ByVerdict["installed"]))
	if len(lines) > 0 {
		fmt.Fprintln(out, "suggestions (nothing was removed; keep means do not delete):")
		for _, l := range lines {
			fmt.Fprintf(out, "    %s\n", l)
		}
	}
	return status.ExitOK
}
