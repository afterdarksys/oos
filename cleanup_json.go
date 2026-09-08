package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// doCleanupJSON is --cleanup's machine form: the plan, and when live, what
// happened. One JSON document either way.
func doCleanupJSON(cfg *Config, env Env, o *opts, items []PlanItem, live bool, now time.Time, out, errw io.Writer) int {
	var planned int64
	for _, it := range items {
		if it.Refused == nil && isDestructive(it.Action) {
			planned += it.Deletable
		}
	}
	doc := map[string]any{
		"live": live, "quarantine": cfg.Policy.Quarantine && !o.permanent, "plan": planJSON(items),
		"planned_bytes": planned, "budget_gb": cfg.Policy.MaxDeleteGBPerRun,
	}
	if !live {
		_ = json.NewEncoder(out).Encode(doc)
		return exitOK
	}
	logf, err := openLog(cfg.Policy.LogFile)
	if err != nil {
		fmt.Fprintf(errw, "oos: cannot open log %s: %v; refusing to act without an audit log\n", cfg.Policy.LogFile, err)
		return exitCritical
	}
	defer logf.Close()
	before, _ := diskUsage(cfg.Volume)
	x := &Executor{Policy: cfg.Policy, Log: logf, Out: io.Discard, Now: time.Now, Run: shellRun, Move: os.Rename, Refs: env.references}
	if cfg.Policy.Quarantine && !o.permanent {
		q, err := openQuarantine(cfg.Policy.QuarantineDir, now, os.Rename)
		if err != nil {
			fmt.Fprintf(errw, "oos: cannot open quarantine: %v\n", err)
			return exitCritical
		}
		x.Q = q
		doc["batch"] = q.Batch
	}
	freed, err := x.Execute(items)
	after, _ := diskUsage(cfg.Volume)
	doc["freed_bytes"] = freed
	doc["free_gb_before"] = before.FreeGB()
	doc["free_gb_after"] = after.FreeGB()
	if err != nil {
		doc["refused"] = err.Error()
	}
	if st, e := loadState(cfg.Policy.StateFile); e == nil {
		st.record("cleanup", after, now)
		_ = saveState(cfg.Policy.StateFile, st)
	}
	_ = json.NewEncoder(out).Encode(doc)
	if err != nil {
		return exitCritical
	}
	_, code := status(cfg.Policy, after)
	return code
}
