package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/dupes"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/status"
)

// doDupes lists identical files under DIR, most wasted bytes first, the
// newest copy of each group named first. --min-mb sets the floor (default
// 10 MB); the age and extension windows apply; --top caps the groups.
func doDupes(cfg *config.Config, env guard.Env, o *opts, now time.Time, out, errw io.Writer) int {
	root := config.ExpandHome(o.dupes, env.Home)
	f, err := filterFromOpts(o)
	if err != nil {
		fmt.Fprintln(errw, "oos:", err)
		return status.ExitUsage
	}
	minBytes := int64(dupes.DefaultMinBytes)
	if o.minMB > 0 {
		minBytes = o.minMB << 20
	}
	res, err := dupes.Find(root, minBytes, f, now)
	if err != nil {
		fmt.Fprintf(errw, "oos: dupes %s: %v\n", root, err)
		return status.ExitUsage
	}
	groups := res.Groups[:size.CapRows(len(res.Groups), f.Top, 0)]
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{
			"root": root, "files_scanned": res.Scanned, "groups": groups, "wasted_bytes": res.Wasted, "elapsed_ms": res.Elapsed.Milliseconds(),
		})
		return status.ExitOK
	}
	fmt.Fprintf(out, "duplicates under %s (%d files over %s scanned in %s%s): %d groups holding %s of repeats\n",
		root, res.Scanned, size.Human(minBytes), res.Elapsed.Round(100*time.Millisecond), f.Describe(), len(res.Groups), size.Human(res.Wasted))
	for _, g := range groups {
		fmt.Fprintf(out, "  %9s x%d  wasted %9s\n", size.Human(g.Bytes), len(g.Files), size.Human(g.Wasted))
		for i, fl := range g.Files {
			mark := "  "
			if i == 0 {
				mark = "* "
			}
			fmt.Fprintf(out, "      %s%s  %s\n", mark, fl.ModTime.Format("2006-01-02"), fl.Path)
		}
	}
	if rest := len(res.Groups) - len(groups); rest > 0 {
		fmt.Fprintf(out, "  (+%d more groups; --top N or -j)\n", rest)
	}
	if len(res.Groups) > 0 {
		fmt.Fprintln(out, "  * newest copy; nothing was removed")
	}
	return status.ExitOK
}
