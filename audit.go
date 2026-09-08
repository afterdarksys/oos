package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// AuditRow is one entry directly under the audited root.
type AuditRow struct {
	Path       string         `json:"path"`
	Bytes      int64          `json:"bytes"`
	ModTime    time.Time      `json:"mtime"` // newest mtime found in the subtree, bounded by the walk
	Hidden     bool           `json:"hidden"`
	IsDir      bool           `json:"dir"`
	Status     string         `json:"status"` // known | protected | unknown | system
	UseCase    string         `json:"use_case,omitempty"`
	Breakdown  []useCaseTotal `json:"breakdown,omitempty"` // for unattributed directories: what is inside
	Suggestion string         `json:"suggestion"`          // human hint, empty when there is nothing to say
}

var cacheName = regexp.MustCompile(`(?i)(^|[._-])(cache|caches|tmp|temp|target|node_modules|deriveddata|\.gradle|\.m2|_cacache|build)($|[._-])`)

// staleAfter is how long an untouched unknown directory sits before the
// audit calls it stale.
const staleAfter = 180 * 24 * time.Hour

// auditHome ranks every direct child of root by size and classifies it
// against the config. It is read-only and never suggests an action it would
// take itself; the suggestions are for the person editing oos.json.
func auditHome(cfg *Config, root string, minBytes int64, now time.Time) ([]AuditRow, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	rows := make([]AuditRow, len(ents))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, de := range ents {
		wg.Add(1)
		go func(i int, de os.DirEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i] = auditOne(cfg, filepath.Join(root, de.Name()), now)
		}(i, de)
	}
	wg.Wait()
	var out []AuditRow
	for _, r := range rows {
		if r.Path == "" || r.Bytes < minBytes {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out, nil
}

func auditOne(cfg *Config, p string, now time.Time) AuditRow {
	fi, err := os.Lstat(p)
	if err != nil {
		return AuditRow{}
	}
	r := AuditRow{Path: p, Hidden: strings.HasPrefix(filepath.Base(p), "."), IsDir: fi.IsDir(), ModTime: fi.ModTime()}
	if fi.Mode()&os.ModeSymlink != 0 {
		r.Status = "symlink"
		r.Suggestion = "symlink, not followed"
		return r
	}
	r.Bytes, _ = pathSize(p)
	if r.IsDir {
		r.ModTime = newestMtime(p, fi.ModTime())
	}
	r.Status, r.Suggestion = classify(cfg, p, r, now)
	r.UseCase, _ = cfg.useCaseFor(p)
	if r.UseCase == "" && r.IsDir && r.Status != "symlink" {
		r.Breakdown = attributeDeep(cfg, p, 2, now)
		r.UseCase = mixedLabel(r.Breakdown)
	}
	return r
}

// newestMtime walks up to two levels below dir to find recent activity, so a
// project directory whose top-level mtime never changes still reads as live.
func newestMtime(dir string, base time.Time) time.Time {
	newest := base
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && strings.Count(strings.TrimPrefix(p, dir), string(filepath.Separator)) >= 2 {
			return filepath.SkipDir
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	return newest
}

func classify(cfg *Config, p string, r AuditRow, now time.Time) (string, string) {
	for _, e := range cfg.entries(nil) {
		if isUnder(p, e.Path) || isUnder(e.Path, p) {
			if e.Action == ActionNever {
				return "known", "listed as never in oos.json"
			}
			if isUnder(e.Path, p) && e.Path != p {
				return "known", fmt.Sprintf("contains known entry %s (%s)", e.Path, e.Action)
			}
			return "known", fmt.Sprintf("%s in oos.json", e.Action)
		}
	}
	for _, nt := range cfg.Policy.NeverTouch {
		if isUnder(p, nt) {
			return "protected", "under never_touch"
		}
	}
	name := filepath.Base(p)
	switch name {
	case "Library", "Applications", "Public", "Sites":
		return "system", "managed by the OS; audit its children with --audit " + p
	}
	if r.IsDir {
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return "unknown", "git repository; add to never_touch"
		}
	}
	var hints []string
	if cacheName.MatchString(name) {
		hints = append(hints, "name looks like a cache or build output; consider adding as rm-contents")
	}
	if r.IsDir && now.Sub(r.ModTime) > staleAfter {
		hints = append(hints, fmt.Sprintf("nothing modified in %d days", int(now.Sub(r.ModTime).Hours()/24)))
	}
	if r.Hidden && r.Bytes >= gb {
		hints = append(hints, "hidden and over 1 GB")
	}
	return "unknown", strings.Join(hints, "; ")
}

func doAudit(cfg *Config, env Env, o *opts, now time.Time, out, errw io.Writer) int {
	root := env.Home
	if o.audit != "" && o.audit != "~" {
		root = expandHome(o.audit, env.Home)
	}
	minMB := o.minMB
	if minMB <= 0 {
		minMB = 100
	}
	rows, err := auditHome(cfg, root, minMB*1024*1024, now)
	if err != nil {
		fmt.Fprintf(errw, "oos: audit %s: %v\n", root, err)
		return exitUsage
	}
	if st, err := loadState(cfg.Policy.StateFile); err == nil {
		st.Audit = rows
		st.AuditRoot = root
		st.AuditedAt = now
		if du, err := diskUsage(cfg.Volume); err == nil {
			st.record("audit", du, now)
		}
		_ = saveState(cfg.Policy.StateFile, st)
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"root": root, "rows": rows, "by_use_case": groupByUseCaseDeep(cfg, rows)})
		return exitOK
	}
	var total, unknown int64
	nUnknown := 0
	for _, r := range rows {
		total += r.Bytes
		if r.Status == "unknown" {
			unknown += r.Bytes
			nUnknown++
		}
	}
	fmt.Fprintf(out, "audit of %s (entries >= %d MB, %d shown, %s):\n", root, minMB, len(rows), human(total))
	for _, r := range rows {
		age := int(now.Sub(r.ModTime).Hours() / 24)
		uc := r.UseCase
		if rs := []rune(uc); len(rs) > 40 {
			uc = string(rs[:39]) + "…"
		}
		fmt.Fprintf(out, "  %9s  %4dd  %-9s %-40s %s\n", human(r.Bytes), age, r.Status, uc, r.Path)
		if r.Suggestion != "" && (o.verbose || r.Status == "unknown" || r.Status == "system") {
			fmt.Fprintf(out, "                    %s\n", r.Suggestion)
		}
	}
	fmt.Fprintf(out, "  %d unknown entries hold %s; each is either a candidate for oos.json or something to leave alone on purpose\n", nUnknown, human(unknown))
	printUseCaseTotals(out, groupByUseCaseDeep(cfg, rows))
	return exitOK
}
