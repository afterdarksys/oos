package plan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/reserve"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
)

// EnsureResult is what an --ensure run (or the daemon's auto-act) did.
type EnsureResult struct {
	TargetGB      float64  `json:"target_gb"`
	StartFreeGB   float64  `json:"start_free_gb"`
	FreeGB        float64  `json:"free_gb"`
	Reached       bool     `json:"reached"`
	Live          bool     `json:"live"`
	RemovedBytes  int64    `json:"removed_allocated_bytes"`
	ObservedDelta int64    `json:"observed_free_delta_bytes"`
	PoorRecovery  bool     `json:"poor_recovery"`
	Diagnostics   []string `json:"diagnostics,omitempty"`
	Steps         []string `json:"steps"`
}

// Phase budgets for Ensure, as shares of policy.operation_timeout: the
// expired-quarantine purge and plan-building each get a slice, execution
// the remainder. Variables for package tests.
var (
	ensurePurgeShare = 0.2
	ensureBuildShare = 0.4
	// PoorRecovery polls statfs this long (ZFS/btrfs free asynchronously).
	recoveryPollWindow   = 5 * time.Second
	recoveryPollInterval = time.Second
)

func share(total time.Duration, f float64) time.Duration { return time.Duration(float64(total) * f) }

// Ensure makes at least target GB free: expired quarantine first, then the
// plan's entries largest first, permanent removals, inside the per-run
// budget, every guard re-checked by the executor. live=false only
// projects. The audit log must open before anything is touched; on a full
// disk the audit falls back to the space reserve, then to stderr.
func Ensure(cfg *config.Config, env guard.Env, target float64, types []string, live bool, now time.Time) (EnsureResult, error) {
	parent := env.Context()
	total := cfg.Policy.OperationTimeout()
	ctx, cancel := context.WithTimeout(parent, total)
	defer cancel()
	env.Ctx = ctx
	if live {
		l, err := Mutation(cfg)
		if err != nil {
			return EnsureResult{}, err
		}
		defer l.Close()
	}
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
	maxBytes := int64(cfg.Policy.MaxDeleteGBPerRun * size.GB)
	remaining := maxBytes
	ts := func() string { return now.UTC().Format(time.RFC3339) }

	var logf *os.File
	var audit *auditLog
	if live {
		reservePath := reserve.Path(cfg.Policy.StateFile)
		_ = reserve.Ensure(reservePath) // best effort, only with room to spare
		logf, err = state.OpenLog(cfg.Policy.LogFile)
		if err != nil {
			return res, fmt.Errorf("cannot open log %s: %v; refusing to act without an audit log", cfg.Policy.LogFile, err)
		}
		defer logf.Close()
		audit = &auditLog{w: logf, reserve: reservePath, permanent: true}
		if err := env.CheckInstall(); err != nil {
			return res, sentinelErr{err, ErrRefused}
		}
		if err := audit.line(fmt.Sprintf("%s ensure start target_gb=%g removals=permanent", ts(), target), true); err != nil {
			return res, err
		}
	}
	timedOut := ""
	var purgeErr error
	if cfg.Policy.Quarantine {
		olderThan := time.Duration(cfg.Policy.QuarantineDays) * 24 * time.Hour
		pctx, pcancel := context.WithTimeout(ctx, share(total, ensurePurgeShare))
		bs, listErr := ListStoreBatchesContext(pctx, cfg.Policy)
		if listErr != nil {
			res.Steps = append(res.Steps, fmt.Sprintf("quarantine listing incomplete: %v", listErr))
		}
		var expired int64
		var expiredCount int
		for _, b := range bs {
			switch {
			case b.Count < 0:
				res.Steps = append(res.Steps, fmt.Sprintf("skip quarantine batch %q: held (%s)", filepath.Join(b.Store, b.Name), b.Held))
			case now.Sub(b.Created) < olderThan:
			case !sameVolume(b.Store, cfg.Volume):
				res.Steps = append(res.Steps, fmt.Sprintf("skip quarantine batch %q: on a different volume than %q", filepath.Join(b.Store, b.Name), cfg.Volume))
			default:
				expired += b.Bytes
				expiredCount++
			}
		}
		if expired > 0 {
			if live {
				var freed int64
				var names []string
				for _, dir := range cfg.Policy.QuarantineStores() {
					if !sameVolume(dir, cfg.Volume) {
						continue
					}
					n, ns, e := purgeBatchesContext(pctx, dir, olderThan, now, false, false, &remaining)
					freed += n
					res.RemovedBytes += n
					names = append(names, ns...)
					purgeErr = errors.Join(purgeErr, e)
				}
				res.Steps = append(res.Steps, fmt.Sprintf("permanently deleted %d expired quarantine batches, %s", len(names), size.Human(freed)))
				errText := "<nil>"
				if purgeErr != nil {
					errText = purgeErr.Error()
					// A stuck batch or tombstone must not stop the cleanup
					// that follows: record it and carry on.
					res.Steps = append(res.Steps, fmt.Sprintf("quarantine purge incomplete, continuing with plan entries: %v", purgeErr))
					res.Diagnostics = append(res.Diagnostics, "quarantine purge: "+purgeErr.Error())
				}
				if errors.Is(pctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
					res.Steps = append(res.Steps, fmt.Sprintf("the quarantine purge phase ran out of time (budget %s); continuing", share(total, ensurePurgeShare).Round(time.Second)))
				}
				if err := audit.line(fmt.Sprintf("%s ensure purge batches=%q recorded_bytes=%d err=%q", ts(), names, freed, errText), true); err != nil {
					pcancel()
					return res, err
				}
				_ = size.SyncAt(cfg.Volume)
				after, diskErr := size.Disk(cfg.Volume)
				if diskErr != nil {
					pcancel()
					return res, diskErr
				}
				projected = int64(after.Free) - int64(du.Free)
			} else {
				res.Steps = append(res.Steps, fmt.Sprintf("would permanently delete %d expired quarantine batches, %s", expiredCount, size.Human(expired)))
				projected += expired
			}
		}
		pcancel()
	}

	// Ensure deletes permanently, so a quarantine filesystem mismatch must not
	// exclude otherwise safe candidates.
	planning := *cfg
	planning.Policy.Quarantine = false
	buildBudget := share(total, ensureBuildShare)
	bctx, bcancel := context.WithTimeout(ctx, buildBudget)
	benv := env
	benv.Ctx = bctx
	items := Build(&planning, benv, types, now)
	if errors.Is(bctx.Err(), context.DeadlineExceeded) {
		timedOut = "plan-building"
		res.Steps = append(res.Steps, fmt.Sprintf("the plan-building phase ran out of time (budget %s); entries not measured in time are skipped", buildBudget.Round(time.Second)))
	}
	bcancel()
	var rm, cmds []Item
	var missing int
	for _, it := range items {
		destructive := config.IsDestructive(it.Action)
		var r *guard.Refusal
		switch {
		case errors.As(it.Refused, &r) && r.Rule == "missing":
			missing++
		case it.Refused != nil:
			res.Steps = append(res.Steps, fmt.Sprintf("skip %q: refused: %v", it.Path, it.Refused))
		case destructive && !sameVolume(it.Path, cfg.Volume):
			res.Steps = append(res.Steps, fmt.Sprintf("skip %q: on a different volume than %q; deleting it frees nothing there", it.Path, cfg.Volume))
		case destructive && it.Deletable > 0:
			rm = append(rm, it)
		case it.Action == config.ActionCommand:
			cmds = append(cmds, it)
		case destructive:
			res.Steps = append(res.Steps, fmt.Sprintf("skip %q: nothing to delete", it.Path))
		}
	}
	if missing > 0 {
		res.Steps = append(res.Steps, fmt.Sprintf("skip %d entries whose path does not exist", missing))
	}
	ordered := append(rm, cmds...)

	x := &Executor{Policy: cfg.Policy, Log: logf, Out: io.Discard, Now: time.Now, Move: os.Rename, Refs: env.References, Home: cfg.Home, Env: &env, Ctx: ctx, audit: audit}
	spent := maxBytes - remaining
	final := du
	var runErrs error
	for _, it := range ordered {
		if projected >= need {
			break
		}
		if live && ctx.Err() != nil {
			if timedOut == "" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				timedOut = "execution"
			}
			runErrs = errors.Join(runErrs, ctx.Err())
			break
		}
		if config.IsDestructive(it.Action) && spent+it.Deletable > maxBytes {
			res.Steps = append(res.Steps, fmt.Sprintf("skip %q: would exceed the %.0f GB per-run budget (policy.max_delete_gb_per_run)", it.Path, cfg.Policy.MaxDeleteGBPerRun))
			continue
		}
		if !live {
			step := fmt.Sprintf("%s %q (%s)", it.Action, it.Path, size.Human(it.Deletable))
			if config.IsDestructive(it.Action) {
				step = fmt.Sprintf("%s %q (%s, would be permanently deleted)", it.Action, it.Path, size.Human(it.Deletable))
			}
			res.Steps = append(res.Steps, step)
			projected += it.Deletable
			spent += it.Deletable
			continue
		}
		before, _ := size.Disk(cfg.Volume)
		x.Policy.MaxDeleteGBPerRun = float64(maxBytes-spent) / size.GB
		_, runErr := x.Execute([]Item{it})
		spent += x.budgetUsed
		res.RemovedBytes += x.budgetUsed
		if err := runErr; err != nil {
			res.Steps = append(res.Steps, fmt.Sprintf("%s %q: %v", it.Action, it.Path, err))
			if errors.Is(err, context.DeadlineExceeded) && timedOut == "" {
				timedOut = "execution"
			}
			runErrs = errors.Join(runErrs, err)
			break
		}
		after, _ := size.Disk(cfg.Volume)
		got := int64(after.Free) - int64(before.Free)
		projected += got
		final = after
		if config.IsDestructive(it.Action) {
			res.Steps = append(res.Steps, fmt.Sprintf("%s %q: permanently deleted, %s freed", it.Action, it.Path, size.Human(got)))
		} else {
			res.Steps = append(res.Steps, fmt.Sprintf("%s %q: %s freed", it.Action, it.Path, size.Human(got)))
		}
	}
	if timedOut != "" {
		res.Steps = append(res.Steps, fmt.Sprintf("stopped early: the %s phase ran out of time (policy.operation_timeout_seconds)", timedOut))
	}
	// finish wraps what went wrong; a reached target with a stuck quarantine
	// batch is a diagnostic, not a failure.
	finish := func() error {
		err := runErrs
		if timedOut != "" && !res.Reached {
			err = errors.Join(err, fmt.Errorf("ensure %s phase ran out of time: %w", timedOut, context.DeadlineExceeded))
		}
		if purgeErr != nil && !res.Reached {
			err = errors.Join(err, purgeErr)
		}
		if err != nil && (timedOut != "" || res.RemovedBytes > 0 || projected > 0) && !errors.Is(err, ErrPartial) {
			err = fmt.Errorf("%w: %w", ErrPartial, err)
		}
		if err != nil && !errors.Is(err, ErrPartial) {
			err = sentinelErr{err, ErrRefused} // nothing was done
		}
		return err
	}
	if live {
		_ = size.SyncAt(cfg.Volume)
		if d, err := size.Disk(cfg.Volume); err == nil {
			final = d
		}
		// ZFS and btrfs return freed blocks asynchronously: poll briefly and
		// keep the best reading before calling the recovery poor.
		if res.RemovedBytes > 0 {
			deadline := time.Now().Add(recoveryPollWindow)
		poll:
			for int64(final.Free)-int64(du.Free) < res.RemovedBytes/10 && time.Now().Before(deadline) {
				select {
				case <-parent.Done():
					break poll
				case <-time.After(recoveryPollInterval):
				}
				if d, err := size.Disk(cfg.Volume); err == nil && d.Free > final.Free {
					final = d
				}
			}
		}
		if _, err := state.Update(cfg.Policy.StateFile, func(st *state.State) { st.Record("ensure", final, now) }); err != nil {
			res.Diagnostics = append(res.Diagnostics, "state not saved: "+err.Error())
		}
		res.ObservedDelta = int64(final.Free) - int64(du.Free)
		if res.RemovedBytes > 0 && res.ObservedDelta < res.RemovedBytes/10 {
			res.PoorRecovery = true
			res.Diagnostics = append(res.Diagnostics, "Less than 10% of removed allocated bytes appeared as free space.", "Snapshots, reflinks, outside hardlinks, open deleted files, delayed accounting, or concurrent writes may retain or consume space.")
		}
		res.FreeGB = final.FreeGB()
		res.Reached = final.FreeGB() >= target
		return res, finish()
	}
	if projected < need {
		res.Steps = append(res.Steps, fmt.Sprintf("short by %s even after every allowed action", size.Human(need-projected)))
	}
	res.FreeGB = size.DiskUsage{Free: du.Free + uint64(projected), Total: du.Total}.FreeGB()
	res.Reached = projected >= need
	return res, finish()
}
