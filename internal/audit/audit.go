package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/size"
)

// Row is one entry directly under the audited root.
type Row struct {
	Path       string         `json:"path"`
	Bytes      int64          `json:"bytes"`
	ModTime    time.Time      `json:"mtime"` // newest mtime found in the subtree, bounded by the walk
	Hidden     bool           `json:"hidden"`
	IsDir      bool           `json:"dir"`
	Status     string         `json:"status"` // known | protected | unknown | system
	UseCase    string         `json:"use_case,omitempty"`
	Breakdown  []UseCaseTotal `json:"breakdown,omitempty"` // for unattributed directories: what is inside
	Suggestion string         `json:"suggestion"`          // human hint, empty when there is nothing to say
	Tags       []string       `json:"tags,omitempty"`      // automatic (stale-90d, big, repo, ...) plus the entry's own
}

// AutoTags are the labels oos can prove about a row from what is on disk.
func AutoTags(cfg *config.Config, r Row, now time.Time) []string {
	var tags []string
	age := now.Sub(r.ModTime)
	switch {
	case age >= 365*24*time.Hour:
		tags = append(tags, "stale-1y")
	case age >= 180*24*time.Hour:
		tags = append(tags, "stale-180d")
	case age >= 90*24*time.Hour:
		tags = append(tags, "stale-90d")
	case age >= 30*24*time.Hour:
		tags = append(tags, "stale-30d")
	}
	if r.Bytes >= 10*size.GB {
		tags = append(tags, "huge")
	} else if r.Bytes >= size.GB {
		tags = append(tags, "big")
	}
	if r.Hidden {
		tags = append(tags, "hidden")
	}
	if r.IsDir && cacheName.MatchString(filepath.Base(r.Path)) {
		tags = append(tags, "build-output")
	}
	if r.IsDir && config.Exists(filepath.Join(r.Path, ".git")) {
		tags = append(tags, "repo")
	}
	if r.Status != "" {
		tags = append(tags, r.Status)
	}
	for _, e := range cfg.Entries(nil) {
		if e.Path == r.Path {
			for _, t := range e.Tags {
				if !HasString(tags, t) {
					tags = append(tags, t)
				}
			}
		}
	}
	return tags
}

func HasString(list []string, s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, x := range list {
		if strings.ToLower(x) == s {
			return true
		}
	}
	return false
}

var cacheName = regexp.MustCompile(`(?i)(^|[._-])(cache|caches|tmp|temp|target|node_modules|deriveddata|\.gradle|\.m2|_cacache|build)($|[._-])`)

// staleAfter is how long an untouched unknown directory sits before the
// audit calls it stale.
const staleAfter = 180 * 24 * time.Hour

// auditHome ranks every direct child of root by size and classifies it
// against the config. It is read-only and never suggests an action it would
// take itself; the suggestions are for the person editing oos.json.
func Scan(cfg *config.Config, root string, minBytes int64, now time.Time) ([]Row, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	rows := make([]Row, len(ents))
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
	var out []Row
	for _, r := range rows {
		if r.Path == "" || r.Bytes < minBytes {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out, nil
}

func auditOne(cfg *config.Config, p string, now time.Time) Row {
	fi, err := os.Lstat(p)
	if err != nil {
		return Row{}
	}
	r := Row{Path: p, Hidden: strings.HasPrefix(filepath.Base(p), "."), IsDir: fi.IsDir(), ModTime: fi.ModTime()}
	if fi.Mode()&os.ModeSymlink != 0 {
		r.Status = "symlink"
		r.Suggestion = "symlink, not followed"
		return r
	}
	r.Bytes, _ = size.PathSize(p)
	if r.IsDir {
		r.ModTime = newestMtime(p, fi.ModTime())
	}
	r.Status, r.Suggestion = classify(cfg, p, r, now)
	r.UseCase, _ = cfg.UseCaseFor(p)
	if r.UseCase == "" && r.IsDir && r.Status != "symlink" {
		r.Breakdown = AttributeDeep(cfg, p, 2, now)
		r.UseCase = MixedLabel(r.Breakdown)
	}
	r.Tags = AutoTags(cfg, r, now)
	return r
}

// ApplyFilter narrows and orders audit rows: age window on the newest
// mtime in the subtree, tag membership, sort key, then the row cap.
func ApplyFilter(rows []Row, f size.Filter, now time.Time, def int) []Row {
	kept := rows[:0]
	for _, r := range rows {
		if !f.KeepTime(r.ModTime, now) {
			continue
		}
		if f.Tag != "" && !HasString(r.Tags, f.Tag) {
			continue
		}
		kept = append(kept, r)
	}
	rows = kept
	sort.SliceStable(rows, size.LessFor(f.SortBy,
		func(i int) int64 { return rows[i].Bytes },
		func(i int) time.Time { return rows[i].ModTime },
		func(i int) string { return rows[i].Path }))
	return rows[:size.CapRows(len(rows), f.Top, def)]
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

func classify(cfg *config.Config, p string, r Row, now time.Time) (string, string) {
	for _, e := range cfg.Entries(nil) {
		if config.IsUnder(p, e.Path) || config.IsUnder(e.Path, p) {
			if e.Action == config.ActionNever {
				return "known", "listed as never in oos.json"
			}
			if config.IsUnder(e.Path, p) && e.Path != p {
				return "known", fmt.Sprintf("contains known entry %s (%s)", e.Path, e.Action)
			}
			return "known", fmt.Sprintf("%s in oos.json", e.Action)
		}
	}
	for _, nt := range cfg.Policy.NeverTouch {
		if config.IsUnder(p, nt) {
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
	if r.Hidden && r.Bytes >= size.GB {
		hints = append(hints, "hidden and over 1 GB")
	}
	return "unknown", strings.Join(hints, "; ")
}
