package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// doByType answers "what kind of files are these bytes": every regular file
// under DIR bucketed by category, largest category first, with the biggest
// files in each. Read-only; the --older-than/--newer-than/--ext windows
// narrow the walk and --top caps the categories shown.
func doByType(cfg *Config, env Env, o *opts, now time.Time, out, errw io.Writer) int {
	root := expandHome(o.byType, env.Home)
	f, err := filterFromOpts(o)
	if err != nil {
		fmt.Fprintln(errw, "oos:", err)
		return exitUsage
	}
	keep := 3
	if o.verbose {
		keep = 10
	}
	rows, total, err := byType(root, f, now, keep)
	if err != nil {
		fmt.Fprintf(errw, "oos: by-type %s: %v\n", root, err)
		return exitUsage
	}
	rows = rows[:capRows(len(rows), f.top, 0)]
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"root": root, "total_bytes": total, "types": rows})
		return exitOK
	}
	var files int
	for _, r := range rows {
		files += r.Files
	}
	fmt.Fprintf(out, "files under %s by type (%d files, %s%s):\n", root, files, human(total), f.describe())
	for _, r := range rows {
		pct := 0.0
		if total > 0 {
			pct = 100 * float64(r.Bytes) / float64(total)
		}
		fmt.Fprintf(out, "  %9s  %5.1f%%  %7d files  %s\n", human(r.Bytes), pct, r.Files, r.Type)
		for _, b := range r.Largest {
			fmt.Fprintf(out, "                 %9s  %s  %s\n", human(b.Bytes), b.ModTime.Format("2006-01-02"), b.Path)
		}
	}
	return exitOK
}
