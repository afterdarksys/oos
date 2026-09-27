package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/worklimit"
	"io"
	"os"
	"time"
)

func doIntegrity(cfg *config.Config, o *opts, out, errw io.Writer) int {
	ctx, cancel := context.WithTimeout(o.Context(), cfg.Policy.OperationTimeout())
	defer cancel()
	ctx = worklimit.With(ctx, cfg.Policy.EntryBudget(), cfg.Policy.VerificationBudget())
	live := o.recoverBatch != "" && o.yes && !o.no
	kind := integrityKind(o)
	if live {
		lock, err := plan.Mutation(cfg)
		if err != nil {
			return failf(o, out, errw, kind, exitFor(err, status.ExitIO), "%v", err)
		}
		defer lock.Close()
		sweepLeftovers(cfg, time.Now(), errw, o.verbose)
	}
	var log *os.File
	if live {
		var err error
		log, err = state.OpenLog(cfg.Policy.LogFile)
		if err != nil {
			return failf(o, out, errw, kind, status.ExitIO, "%v", err)
		}
		defer log.Close()
		if _, err = fmt.Fprintf(log, "%s recover intent batch=%q\n", time.Now().UTC().Format(time.RFC3339), o.recoverBatch); err != nil {
			return failf(o, out, errw, kind, status.ExitIO, "%v", err)
		}
		if err = log.Sync(); err != nil {
			return failf(o, out, errw, kind, status.ExitIO, "%v", err)
		}
	}
	type result struct {
		Store        string            `json:"store"`
		Verification plan.Verification `json:"verification"`
		Error        string            `json:"error,omitempty"`
		// Held is why a batch could not be verified: a stuck tombstone
		// ("purge incomplete: ...") has no manifest left to check.
		Held string `json:"held,omitempty"`
	}
	results := []result{}
	code := status.ExitOK
	stores := cfg.Policy.QuarantineStores()
	if o.recoverBatch != "" {
		dir, err := plan.FindBatch(cfg.Policy, o.recoverBatch)
		if err != nil {
			return failf(o, out, errw, kind, status.ExitCritical, "%v", err)
		}
		stores = []string{dir}
	}
	for _, dir := range stores {
		batches, err := plan.ListBatches(dir)
		if err != nil {
			results = append(results, result{Store: dir, Error: err.Error()})
			code = status.ExitCritical
			continue
		}
		for _, b := range batches {
			if o.recoverBatch != "" && b.Name != o.recoverBatch {
				continue
			}
			if b.Tombstone {
				// a purge committed this batch to deletion and could not
				// finish: nothing to verify, but it is not clean either
				results = append(results, result{Store: dir, Verification: plan.Verification{Batch: b.Name, Issues: []string{}, Entries: []plan.EntryVerification{}}, Held: b.Held})
				code = status.ExitCritical
				continue
			}
			var v plan.Verification
			if o.recoverBatch != "" {
				v, err = plan.RecoverBatchContext(ctx, dir, b.Name, live)
			} else {
				v, err = plan.VerifyBatchContext(ctx, dir, b.Name, o.deep)
			}
			r := result{Store: dir, Verification: v}
			if err != nil {
				r.Error = err.Error()
				code = status.ExitCritical
				if errors.Is(err, plan.ErrStoreLock) {
					code = status.ExitIO // an unsafe lock file, as on cleanup and purge
				}
			}
			if len(v.Issues) > 0 {
				code = status.ExitCritical
			}
			for _, e := range v.Entries {
				if e.Status != "quarantined" && e.Status != "source-intact" {
					code = status.ExitCritical
				}
			}
			results = append(results, r)
		}
	}
	if o.recoverBatch != "" && len(results) == 0 {
		return failf(o, out, errw, kind, status.ExitUsage, "batch not found")
	}
	if live {
		if _, err := fmt.Fprintf(log, "%s recover complete batch=%q status=%d\n", time.Now().UTC().Format(time.RFC3339), o.recoverBatch, code); err != nil {
			return failf(o, out, errw, kind, status.ExitIO, "%v", err)
		}
	}
	if o.jsonOut {
		json.NewEncoder(out).Encode(map[string]any{"kind": integrityKind(o), "live": live, "batches": results})
		return code
	}
	for _, r := range results {
		fmt.Fprintf(out, "%s / %s (manifest v%d)\n", r.Store, r.Verification.Batch, r.Verification.Version)
		if r.Held != "" {
			fmt.Fprintln(out, "  held:", r.Held)
			fmt.Fprintln(out, "  the next --purge retries it; fix the cause first (permissions or an immutable flag)")
			continue
		}
		if r.Error != "" {
			fmt.Fprintln(out, "  error:", r.Error)
		}
		for _, issue := range r.Verification.Issues {
			fmt.Fprintln(out, "  issue:", issue)
		}
		for _, e := range r.Verification.Entries {
			fmt.Fprintf(out, "  %-16s %s %s\n", e.Status, e.From, e.Detail)
		}
	}
	if o.recoverBatch != "" && !live {
		fmt.Fprintln(out, "dry-run: no journal changed; add --yes to reconcile proven identities")
	}
	return code
}
func doFilesystem(cfg *config.Config, o *opts, out, errw io.Writer) int {
	path := config.ExpandHome(o.filesystem, cfg.Home)
	caps, err := size.Filesystem(path)
	if err != nil {
		return failf(o, out, errw, "filesystem", status.ExitUsage, "%v", err)
	}
	disk, err := size.Disk(path)
	if err != nil {
		return failf(o, out, errw, "filesystem", status.ExitCritical, "%v", err)
	}
	doc := map[string]any{"kind": "filesystem", "path": path, "capabilities": caps, "disk": disk}
	ctx, cancel := context.WithTimeout(o.Context(), cfg.Policy.OperationTimeout())
	defer cancel()
	ctx = worklimit.With(ctx, cfg.Policy.EntryBudget(), cfg.Policy.VerificationBudget())
	code := status.ExitOK
	if o.deep {
		a, err := size.Account(ctx, nil, path)
		doc["accounting"] = a
		if err != nil {
			doc["error"] = err.Error()
			code = status.ExitCritical
		}
	}
	if o.fsDetails {
		d, err := size.Diagnostics(ctx, path)
		doc["diagnostics"] = d
		if err != nil {
			doc["diagnostics_error"] = err.Error()
		}
	}
	if o.jsonOut {
		json.NewEncoder(out).Encode(doc)
	} else {
		b, _ := json.MarshalIndent(doc, "", "  ")
		fmt.Fprintln(out, string(b))
	}
	return code
}

// integrityKind names the JSON "kind" of --verify-quarantine and --recover.
func integrityKind(o *opts) string {
	if o.recoverBatch != "" {
		return "recover"
	}
	return "verify-quarantine"
}
