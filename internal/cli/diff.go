package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
)

type diffRow struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Prev  int64  `json:"prev_bytes"`
	Now   int64  `json:"now_bytes"`
	Delta int64  `json:"delta_bytes"`
	New   bool   `json:"new,omitempty"`
}

// doDiff sizes the known entries and reports growth against the sizes the
// state file recorded last time, plus the free-space change since the last
// recorded reading. It then records the new sizes.
func doDiff(cfg *config.Config, env guard.Env, o *opts, now time.Time, out, errw io.Writer) int {
	if _, err := state.Load(cfg.Policy.StateFile); err != nil {
		fmt.Fprintf(errw, "oos: load state: %v\n", err)
		return status.ExitUsage
	}
	items := plan.Build(cfg, env, splitTypes(o.types), now)
	du, duErr := size.Disk(cfg.Volume)
	// Compare and record under the state lock so a concurrent writer's sizes
	// are neither lost nor used as a stale baseline.
	var prevFree float64
	var prevAt time.Time
	var rows []diffRow
	applied := false
	_, err := state.Update(cfg.Policy.StateFile, func(st *state.State) {
		applied = true
		prevFree, prevAt = st.FreeGB, st.UpdatedAt
		rows = make([]diffRow, 0, len(items))
		for _, it := range items {
			prev, had := st.Known[it.Path]
			rows = append(rows, diffRow{Path: it.Path, Type: it.Type, Prev: prev, Now: it.Bytes, Delta: it.Bytes - prev, New: !had})
			if it.Bytes > 0 || had {
				st.Known[it.Path] = it.Bytes
			}
		}
		if duErr == nil {
			st.Volume = cfg.Volume
			st.Record("diff", du, now)
		}
	})
	if !applied {
		fmt.Fprintf(errw, "oos: load state: %v\n", err)
		return status.ExitUsage
	}
	if err != nil {
		fmt.Fprintf(errw, "oos: save state: %v\n", err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Delta > rows[j].Delta })
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{
			"since": prevAt, "prev_free_gb": prevFree, "free_gb": du.FreeGB(), "rows": rows,
		})
		return status.ExitOK
	}
	if prevAt.IsZero() {
		fmt.Fprintln(out, "no previous sizes recorded; this run is the baseline")
	} else {
		fmt.Fprintf(out, "since %s (%s ago)\n", prevAt.Local().Format("2006-01-02 15:04"), now.Sub(prevAt).Round(time.Minute))
		if duErr == nil {
			fmt.Fprintf(out, "  volume free %.1f GB -> %.1f GB (%+.1f GB)\n", prevFree, du.FreeGB(), du.FreeGB()-prevFree)
		}
	}
	var grew, shrank int64
	for _, r := range rows {
		tag := ""
		if r.New {
			tag = "  (new)"
		}
		fmt.Fprintf(out, "  %10s  %9s  %-8s %s%s\n", size.HumanDelta(r.Delta), size.Human(r.Now), r.Type, r.Path, tag)
		if r.Delta > 0 {
			grew += r.Delta
		} else {
			shrank -= r.Delta
		}
	}
	fmt.Fprintf(out, "  known entries grew %s, shrank %s\n", size.Human(grew), size.Human(shrank))
	if duErr == nil && !prevAt.IsZero() {
		unexplained := int64((prevFree-du.FreeGB())*size.GB) - (grew - shrank)
		if unexplained > size.GB {
			fmt.Fprintf(out, "  %s of the free-space drop is outside known entries; try --scan on a suspect root\n", size.Human(unexplained))
		}
	}
	return status.ExitOK
}
