package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/afterdarksys/oos/internal/status"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
)

// doCleanupJSON is --cleanup's machine form: the plan, and when live, what
// happened. One JSON document either way.
func doCleanupJSON(cfg *config.Config, env guard.Env, o *opts, items []plan.Item, live bool, now time.Time, out, errw io.Writer) int {
	var planned int64
	for _, it := range items {
		if it.Refused == nil && config.IsDestructive(it.Action) {
			planned += it.Deletable
		}
	}
	doc := map[string]any{
		"live": live, "quarantine": cfg.Policy.Quarantine && !o.permanent, "plan": planJSON(items),
		"planned_bytes": planned, "budget_gb": cfg.Policy.MaxDeleteGBPerRun,
	}
	if !live {
		_ = json.NewEncoder(out).Encode(doc)
		return status.ExitOK
	}
	logf, err := state.OpenLog(cfg.Policy.LogFile)
	if err != nil {
		fmt.Fprintf(errw, "oos: cannot open log %s: %v; refusing to act without an audit log\n", cfg.Policy.LogFile, err)
		return status.ExitCritical
	}
	defer logf.Close()
	before, _ := size.Disk(cfg.Volume)
	x := &plan.Executor{Policy: cfg.Policy, Log: logf, Out: io.Discard, Now: time.Now, Run: plan.ShellRun, Move: os.Rename, Refs: env.References}
	if cfg.Policy.Quarantine && !o.permanent {
		q, err := plan.OpenQuarantine(cfg.Policy.QuarantineDir, now, os.Rename)
		if err != nil {
			fmt.Fprintf(errw, "oos: cannot open quarantine: %v\n", err)
			return status.ExitCritical
		}
		x.Q = q
		doc["batch"] = q.Batch
	}
	freed, err := x.Execute(items)
	after, _ := size.Disk(cfg.Volume)
	doc["freed_bytes"] = freed
	doc["free_gb_before"] = before.FreeGB()
	doc["free_gb_after"] = after.FreeGB()
	if err != nil {
		doc["refused"] = err.Error()
	}
	if st, e := state.Load(cfg.Policy.StateFile); e == nil {
		st.Record("cleanup", after, now)
		_ = state.Save(cfg.Policy.StateFile, st)
	}
	_ = json.NewEncoder(out).Encode(doc)
	if err != nil {
		return status.ExitCritical
	}
	_, code := status.Of(cfg.Policy, after)
	return code
}
