package plan

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
)

// EnsureResult is what an --ensure run (or the daemon's auto-act) did.
type EnsureResult struct {
	TargetGB    float64  `json:"target_gb"`
	StartFreeGB float64  `json:"start_free_gb"`
	FreeGB      float64  `json:"free_gb"`
	Reached     bool     `json:"reached"`
	Live        bool     `json:"live"`
	Steps       []string `json:"steps"`
}

// Ensure makes at least target GB free: expired quarantine first, then the
// plan's entries largest first, permanent removals, inside the per-run
// budget, every guard re-checked by the executor. live=false only
// projects. The audit log must open before anything is touched.
func Ensure(cfg *config.Config, env guard.Env, target float64, types []string, live bool, now time.Time) (EnsureResult, error) {
	du, err := size.Disk(cfg.Volume)
	if err != nil {
		return EnsureResult{}, fmt.Errorf("statfs %s: %v", cfg.Volume, err)
	}
	res := EnsureResult{TargetGB: target, StartFreeGB: du.FreeGB(), FreeGB: du.FreeGB(), Live: live}
	if du.FreeGB() >= target {
		res.Reached = true
		res.Steps = []string{fmt.Sprintf("already %.1f GB free", du.FreeGB())}
		return res, nil
	}
	need := int64((target - du.FreeGB()) * size.GB)
	var projected int64

	if cfg.Policy.Quarantine {
		olderThan := time.Duration(cfg.Policy.QuarantineDays) * 24 * time.Hour
		bs, _ := ListBatches(cfg.Policy.QuarantineDir)
		var expired int64
		for _, b := range bs {
			if b.Count >= 0 && now.Sub(b.Created) >= olderThan {
				expired += b.Bytes
			}
		}
		if expired > 0 {
			if live {
				freed, names, err := PurgeBatches(cfg.Policy.QuarantineDir, olderThan, now, false)
				res.Steps = append(res.Steps, fmt.Sprintf("purged %d expired quarantine batches, %s (err=%v)", len(names), size.Human(freed), err))
				projected += freed
			} else {
				res.Steps = append(res.Steps, fmt.Sprintf("purge %d expired quarantine batches, %s", len(bs), size.Human(expired)))
				projected += expired
			}
		}
	}

	items := Build(cfg, env, types, now)
	var rm, cmds []Item
	for _, it := range items {
		if it.Refused != nil {
			continue
		}
		if config.IsDestructive(it.Action) && it.Deletable > 0 {
			rm = append(rm, it)
		} else if it.Action == config.ActionCommand {
			cmds = append(cmds, it)
		}
	}
	ordered := append(rm, cmds...)

	var logf *os.File
	if live {
		logf, err = state.OpenLog(cfg.Policy.LogFile)
		if err != nil {
			return res, fmt.Errorf("cannot open log %s: %v; refusing to act without an audit log", cfg.Policy.LogFile, err)
		}
		defer logf.Close()
	}
	x := &Executor{Policy: cfg.Policy, Log: logf, Out: io.Discard, Now: time.Now, Run: ShellRun, Move: os.Rename, Refs: env.References}
	var spent int64
	maxBytes := int64(cfg.Policy.MaxDeleteGBPerRun * size.GB)
	final := du
	for _, it := range ordered {
		if projected >= need {
			break
		}
		if config.IsDestructive(it.Action) && spent+it.Deletable > maxBytes {
			res.Steps = append(res.Steps, fmt.Sprintf("skip %s: would exceed the %.0f GB per-run budget", it.Path, cfg.Policy.MaxDeleteGBPerRun))
			continue
		}
		if !live {
			res.Steps = append(res.Steps, fmt.Sprintf("%s %s (%s)", it.Action, it.Path, size.Human(it.Deletable)))
			projected += it.Deletable
			spent += it.Deletable
			continue
		}
		before, _ := size.Disk(cfg.Volume)
		if _, err := x.Execute([]Item{it}); err != nil {
			res.Steps = append(res.Steps, fmt.Sprintf("%s %s: %v", it.Action, it.Path, err))
			continue
		}
		after, _ := size.Disk(cfg.Volume)
		got := int64(after.Free) - int64(before.Free)
		spent += it.Deletable
		projected += got
		final = after
		res.Steps = append(res.Steps, fmt.Sprintf("%s %s: %s freed", it.Action, it.Path, size.Human(got)))
	}
	if live {
		final, _ = size.Disk(cfg.Volume)
		if st, err := state.Load(cfg.Policy.StateFile); err == nil {
			st.Record("ensure", final, now)
			_ = state.Save(cfg.Policy.StateFile, st)
		}
		res.FreeGB = final.FreeGB()
		res.Reached = final.FreeGB() >= target
		return res, nil
	}
	if projected < need {
		res.Steps = append(res.Steps, fmt.Sprintf("short by %s even after every allowed action", size.Human(need-projected)))
	}
	res.FreeGB = size.DiskUsage{Free: du.Free + uint64(projected), Total: du.Total}.FreeGB()
	res.Reached = projected >= need
	return res, nil
}
