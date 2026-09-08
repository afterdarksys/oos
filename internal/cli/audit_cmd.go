package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/afterdarksys/oos/internal/audit"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
)

func doAudit(cfg *config.Config, env guard.Env, o *opts, now time.Time, out, errw io.Writer) int {
	root := env.Home
	if o.audit != "" && o.audit != "~" {
		root = config.ExpandHome(o.audit, env.Home)
	}
	minMB := o.minMB
	if minMB <= 0 {
		minMB = 100
	}
	f, err := filterFromOpts(o)
	if err != nil {
		fmt.Fprintln(errw, "oos:", err)
		return status.ExitUsage
	}
	rows, err := audit.Scan(cfg, root, minMB*1024*1024, now)
	if err != nil {
		fmt.Fprintf(errw, "oos: audit %s: %v\n", root, err)
		return status.ExitUsage
	}
	rows = audit.ApplyFilter(rows, f, now, 0)
	if st, err := state.Load(cfg.Policy.StateFile); err == nil {
		st.Audit = rows
		st.AuditRoot = root
		st.AuditedAt = now
		if du, err := size.Disk(cfg.Volume); err == nil {
			st.Record("audit", du, now)
		}
		_ = state.Save(cfg.Policy.StateFile, st)
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"root": root, "rows": rows, "by_use_case": audit.GroupByUseCaseDeep(cfg, rows)})
		return status.ExitOK
	}
	var total, unknown int64
	nUnknown := 0
	for _, r := range rows {
		total += r.Bytes
		if r.Status == "unknown" {
			unknown += r.Bytes
			nUnknown++
		}
	}
	fmt.Fprintf(out, "audit of %s (entries >= %d MB, %d shown, %s%s):\n", root, minMB, len(rows), size.Human(total), f.Describe())
	for _, r := range rows {
		age := int(now.Sub(r.ModTime).Hours() / 24)
		uc := r.UseCase
		if rs := []rune(uc); len(rs) > 40 {
			uc = string(rs[:39]) + "…"
		}
		tags := ""
		if len(r.Tags) > 0 {
			tags = "  [" + strings.Join(r.Tags, " ") + "]"
		}
		fmt.Fprintf(out, "  %9s  %4dd  %-9s %-40s %s%s\n", size.Human(r.Bytes), age, r.Status, uc, r.Path, tags)
		if r.Suggestion != "" && (o.verbose || r.Status == "unknown" || r.Status == "system") {
			fmt.Fprintf(out, "                    %s\n", r.Suggestion)
		}
	}
	fmt.Fprintf(out, "  %d unknown entries hold %s; each is either a candidate for oos.json or something to leave alone on purpose\n", nUnknown, size.Human(unknown))
	audit.PrintUseCaseTotals(out, audit.GroupByUseCaseDeep(cfg, rows))
	return status.ExitOK
}
