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
	var planned, reclaimable int64
	for _, it := range items {
		if it.Refused == nil && config.IsDestructive(it.Action) {
			planned += it.Deletable
			reclaimable += it.Reclaimable
		}
	}
	doc := map[string]any{
		"kind": "cleanup", "refused": refusedRows(items), "live": live, "quarantine": cfg.Policy.Quarantine && !o.permanent, "plan": planJSON(items),
		"planned_bytes": planned, "reclaimable_bytes": reclaimable, "budget_gb": cfg.Policy.MaxDeleteGBPerRun,
	}
	if !live {
		if n := otherUserProcesses(); n > 0 {
			doc["other_user_processes_not_inspected"] = n
		}
		_ = json.NewEncoder(out).Encode(doc)
		return status.ExitOK
	}
	fail := func(code int, err error) int {
		doc["error"], doc["error_kind"] = err.Error(), status.Kind(code)
		_ = json.NewEncoder(out).Encode(doc)
		return code
	}
	logf, err := state.OpenLog(cfg.Policy.LogFile)
	if err != nil {
		fmt.Fprintf(errw, "oos: cannot open log %q: %v; refusing to act without an audit log\n", cfg.Policy.LogFile, err)
		return fail(status.ExitIO, fmt.Errorf("cannot open log: %w", err))
	}
	defer logf.Close()
	before, _ := size.Disk(cfg.Volume)
	x := &plan.Executor{Policy: cfg.Policy, Log: logf, Out: io.Discard, Now: time.Now, Move: os.Rename, Refs: env.References, Home: cfg.Home, Env: &env, Ctx: env.Context()}
	if cfg.Policy.Quarantine && !o.permanent {
		stores, err := plan.OpenStores(cfg, now)
		if err != nil {
			fmt.Fprintf(errw, "oos: cannot open quarantine: %v\n", err)
			return fail(exitFor(err, status.ExitIO), fmt.Errorf("cannot open quarantine: %w", err)) // a busy store lock is 4
		}
		x.Stores = stores
		x.Q = stores.Primary()
		defer stores.DiscardEmpty()
		doc["batch"] = x.Q.Batch
	}
	freed, err := x.Execute(items)
	after, _ := size.Disk(cfg.Volume)
	if x.Q != nil && x.Q.Empty() {
		_ = x.Q.Discard()
		delete(doc, "batch")
	}
	if x.Stores != nil {
		doc["batches"] = x.Stores.Batches()
	}
	doc["recorded_bytes"] = freed
	doc["freed_bytes"] = freed // kept for readers of 0.6; the honest number is free_gb_after - free_gb_before
	doc["free_gb_before"] = before.FreeGB()
	doc["free_gb_after"] = after.FreeGB()
	code := status.ExitOK
	if err != nil {
		// kept a string for readers of 0.7: the run-level refusal; the
		// per-item refusals are in "refused"
		// 5 only when some items were done; nothing done is 2 "refused"
		var kind string
		code, kind = ranExit(err, status.ExitCritical)
		doc["error"], doc["error_kind"] = err.Error(), kind
		doc["refused"] = append(doc["refused"].([]map[string]any), map[string]any{"path": "", "reason": err.Error()})
	}
	if n := otherUserProcesses(); n > 0 {
		doc["other_user_processes_not_inspected"] = n
	}
	if _, e := state.Update(cfg.Policy.StateFile, func(st *state.State) { st.Record("cleanup", after, now) }); e != nil {
		fmt.Fprintf(errw, "oos: save state: %v\n", e)
	}
	_ = json.NewEncoder(out).Encode(doc)
	if err != nil {
		return code
	}
	_, code = status.Of(cfg.Policy, after)
	return code
}

// refusedRows lists every planned item a guard refused, with its reason.
func refusedRows(items []plan.Item) []map[string]any {
	rows := []map[string]any{}
	for _, it := range items {
		if it.Refused != nil {
			rows = append(rows, setPath(map[string]any{"reason": it.Refused.Error()}, it.Path))
		}
	}
	return rows
}
