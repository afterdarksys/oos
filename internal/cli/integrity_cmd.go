package cli

import (
	"context"
	"encoding/json"
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
	if live {
		lock, err := plan.Mutation(cfg)
		if err != nil {
			fmt.Fprintln(errw, err)
			return status.ExitCritical
		}
		defer lock.Close()
	}
	var log *os.File
	if live {
		var err error
		log, err = state.OpenLog(cfg.Policy.LogFile)
		if err != nil {
			fmt.Fprintln(errw, err)
			return status.ExitCritical
		}
		defer log.Close()
		if _, err = fmt.Fprintf(log, "%s recover intent batch=%s\n", time.Now().UTC().Format(time.RFC3339), o.recoverBatch); err != nil {
			fmt.Fprintln(errw, err)
			return status.ExitCritical
		}
		if err = log.Sync(); err != nil {
			fmt.Fprintln(errw, err)
			return status.ExitCritical
		}
	}
	type result struct {
		Store        string            `json:"store"`
		Verification plan.Verification `json:"verification"`
		Error        string            `json:"error,omitempty"`
	}
	results := []result{}
	code := status.ExitOK
	stores := cfg.Policy.QuarantineStores()
	if o.recoverBatch != "" {
		dir, err := plan.FindBatch(cfg.Policy, o.recoverBatch)
		if err != nil {
			fmt.Fprintln(errw, err)
			return status.ExitCritical
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
		fmt.Fprintln(errw, "batch not found")
		return status.ExitUsage
	}
	if live {
		if _, err := fmt.Fprintf(log, "%s recover complete batch=%s status=%d\n", time.Now().UTC().Format(time.RFC3339), o.recoverBatch, code); err != nil {
			fmt.Fprintln(errw, err)
			return status.ExitCritical
		}
	}
	if o.jsonOut {
		json.NewEncoder(out).Encode(map[string]any{"live": live, "batches": results})
		return code
	}
	for _, r := range results {
		fmt.Fprintf(out, "%s / %s (manifest v%d)\n", r.Store, r.Verification.Batch, r.Verification.Version)
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
		fmt.Fprintln(errw, err)
		return status.ExitUsage
	}
	disk, err := size.Disk(path)
	if err != nil {
		fmt.Fprintln(errw, err)
		return status.ExitCritical
	}
	doc := map[string]any{"path": path, "capabilities": caps, "disk": disk}
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
