package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/trash"
)

// doTrash measures the operating system's trash. With --empty-trash it
// deletes the contents permanently. That is not quarantine: the space
// comes back immediately and there is no --restore.
func doTrash(cfg *config.Config, env guard.Env, o *opts, now time.Time, out, errw io.Writer) int {
	bins := trash.Locate(env.Home)
	var total int64
	for _, b := range bins {
		if b.Error == "" {
			total += b.Bytes
		}
	}
	if !o.emptyTrash {
		return printTrash(bins, total, o.jsonOut, out)
	}
	live := o.yes && !o.no
	if !cfg.Policy.RequireYes {
		live = !o.no
	}
	maxBytes := int64(cfg.Policy.MaxDeleteGBPerRun * float64(size.GB))
	if total > maxBytes {
		fmt.Fprintf(errw, "oos: trash holds %s, policy max is %.1f GB per run\n", size.Human(total), cfg.Policy.MaxDeleteGBPerRun)
		return status.ExitCritical
	}
	if !live {
		if o.jsonOut {
			_ = json.NewEncoder(out).Encode(map[string]any{
				"bins": bins, "total_bytes": total, "dry_run": true, "permanent": true,
			})
			return status.ExitOK
		}
		printTrash(bins, total, false, out)
		fmt.Fprintln(out, "dry-run: trash was not emptied. This is a permanent delete, not quarantine. Add --yes to empty it.")
		return status.ExitOK
	}
	logf, err := state.OpenLog(cfg.Policy.LogFile)
	if err != nil {
		fmt.Fprintf(errw, "oos: cannot open log %s: %v; refusing to empty the trash without an audit log\n", cfg.Policy.LogFile, err)
		return status.ExitCritical
	}
	defer logf.Close()
	uid := os.Getuid()
	type result struct {
		Path    string `json:"path"`
		Bytes   int64  `json:"bytes"`
		Entries int    `json:"entries"`
		Error   string `json:"error,omitempty"`
	}
	var results []result
	var freed int64
	code := status.ExitOK
	for _, b := range bins {
		if b.Error != "" {
			fmt.Fprintf(logf, "%s empty-trash %s skipped err=%s\n", now.UTC().Format(time.RFC3339), b.Path, b.Error)
			results = append(results, result{Path: b.Path, Error: b.Error})
			continue
		}
		nBytes, n, err := trash.Empty(b.Path, env.Home, uid)
		res := result{Path: b.Path, Bytes: nBytes, Entries: n}
		if err != nil {
			res.Error = err.Error()
			code = status.ExitCritical
		} else {
			freed += nBytes
		}
		results = append(results, res)
		fmt.Fprintf(logf, "%s empty-trash %s bytes=%d entries=%d err=%v\n", now.UTC().Format(time.RFC3339), b.Path, nBytes, n, err)
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"bins": results, "freed_bytes": freed, "permanent": true})
		return code
	}
	fmt.Fprintf(out, "emptied trash: %s across %d bin(s). This was a permanent delete, not quarantine.\n", size.Human(freed), len(results))
	for _, r := range results {
		if r.Error != "" {
			fmt.Fprintf(out, "  %s  %s\n", r.Path, r.Error)
			continue
		}
		fmt.Fprintf(out, "  %9s  %d items  %s\n", size.Human(r.Bytes), r.Entries, r.Path)
	}
	return code
}

func printTrash(bins []trash.Bin, total int64, jsonOut bool, out io.Writer) int {
	if jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"bins": bins, "total_bytes": total})
		return status.ExitOK
	}
	fmt.Fprintln(out, "trash (emptying is permanent and is not quarantine):")
	if len(bins) == 0 {
		fmt.Fprintln(out, "  no trash")
		return status.ExitOK
	}
	for _, b := range bins {
		if b.Error != "" {
			fmt.Fprintf(out, "  %9s  %-12s %s  %s\n", "?", b.Kind, b.Path, b.Error)
			continue
		}
		fmt.Fprintf(out, "  %9s  %5d items  %-12s %s\n", size.Human(b.Bytes), b.Entries, b.Kind, b.Path)
	}
	fmt.Fprintf(out, "total %s\n", size.Human(total))
	fmt.Fprintln(out, "empty with: oos --empty-trash --yes")
	return status.ExitOK
}
