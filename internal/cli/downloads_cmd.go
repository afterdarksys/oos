package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/triage"
)

// doDownloads judges every entry in a downloads folder (default
// ~/Downloads): installers whose app is installed, archives extracted
// beside them, "(2)" copies, partial downloads, app bundles, stale and big.
func doDownloads(cfg *config.Config, env guard.Env, o *opts, now time.Time, out, errw io.Writer) int {
	root := filepath.Join(env.Home, "Downloads")
	if o.downloads != "" && o.downloads != "~/Downloads" && o.downloads != "default" {
		root = config.ExpandHome(o.downloads, env.Home)
	}
	f, err := filterFromOpts(o)
	if err != nil {
		fmt.Fprintln(errw, "oos:", err)
		return status.ExitUsage
	}
	res, err := triage.Scan(root, env.Home, f, now)
	if err != nil {
		fmt.Fprintf(errw, "oos: downloads %s: %v\n", root, err)
		return status.ExitUsage
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{
			"root": root, "rows": res.Rows, "total_bytes": res.Total, "bytes_by_verdict": res.ByVerdict, "elapsed_ms": res.Elapsed.Milliseconds(),
		})
		return status.ExitOK
	}
	var b strings.Builder
	triage.Print(&b, res, now, o.verbose, f.Top)
	io.WriteString(out, b.String())
	return status.ExitOK
}
