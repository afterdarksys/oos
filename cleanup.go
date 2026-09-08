package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// PlanItem is one entry after sizing and guarding.
type PlanItem struct {
	Entry
	Bytes     int64       // total under the path
	Deletable int64       // what a live run would remove
	Children  []ChildPlan // rm-stale-children only
	Refused   error       // non-nil means oos will not act on this entry
}

// buildPlan sizes every candidate entry in parallel and runs the guards.
// Entries marked never are included so the report shows them, but refused.
func buildPlan(cfg *Config, env Env, types []string, now time.Time) []PlanItem {
	return buildPlanTagged(cfg, env, types, "", now)
}

// buildPlanTagged is buildPlan narrowed to entries carrying tag ("" = all).
func buildPlanTagged(cfg *Config, env Env, types []string, tag string, now time.Time) []PlanItem {
	ents := cfg.entriesTagged(types, tag)
	items := make([]PlanItem, len(ents))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, e := range ents {
		wg.Add(1)
		go func(i int, e Entry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			items[i] = planItem(cfg, env, e, now)
		}(i, e)
	}
	wg.Wait()
	sort.SliceStable(items, func(a, b int) bool { return items[a].Bytes > items[b].Bytes })
	return items
}

func planItem(cfg *Config, env Env, e Entry, now time.Time) PlanItem {
	it := PlanItem{Entry: e}
	if _, err := os.Lstat(e.Path); err != nil {
		if os.IsNotExist(err) {
			it.Refused = refuse("missing", "path does not exist")
		} else {
			it.Refused = refuse("stat", "%v", err)
		}
		return it
	}
	it.Refused = env.checkDeletable(cfg.Policy, e)

	switch e.Action {
	case ActionRmStaleChilds:
		if it.Refused != nil {
			it.Bytes, _ = pathSize(e.Path)
			return it
		}
		refs, err := env.references()
		if err != nil {
			it.Refused = refuse("references", "%v", err)
			it.Bytes, _ = pathSize(e.Path)
			return it
		}
		children, err := classifyChildren(e.Path, time.Duration(e.StaleAfterHours)*time.Hour, refs, now)
		if err != nil {
			it.Refused = refuse("children", "%v", err)
			return it
		}
		it.Children = children
		for _, c := range children {
			it.Bytes += c.Bytes
			if c.Keep == "" {
				it.Deletable += c.Bytes
			}
		}
	default:
		b, err := pathSize(e.Path)
		if err != nil && it.Refused == nil {
			it.Refused = refuse("size", "%v", err)
		}
		it.Bytes = b
		if it.Refused == nil && (e.Action == ActionRm || e.Action == ActionRmContents) {
			it.Deletable = b
		}
	}

	// Quarantine moves with rename, which cannot cross devices. Refuse now,
	// visibly in the plan, rather than failing halfway through a live run.
	if it.Refused == nil && cfg.Policy.Quarantine && isDestructive(e.Action) {
		if err := sameDevice(e.Path, cfg.Policy.QuarantineDir); err != nil {
			it.Refused = refuse("quarantine", "%v", err)
		}
	}
	return it
}

// sameDevice checks that p and the quarantine dir (or its nearest existing
// ancestor) live on one filesystem.
func sameDevice(p, qdir string) error {
	pd, err := deviceOfPath(p)
	if err != nil {
		return err
	}
	qd, err := deviceOfPath(qdir)
	if err != nil {
		return err
	}
	if pd != qd {
		return fmt.Errorf("%s and quarantine_dir %s are on different devices; rename cannot cross them", p, qdir)
	}
	return nil
}

func deviceOfPath(p string) (uint64, error) {
	p = filepath.Clean(p)
	for {
		fi, err := os.Lstat(p)
		if err == nil {
			dev, ok := deviceOf(fi)
			if !ok {
				return 0, errors.New("device id unavailable on this platform")
			}
			return dev, nil
		}
		if !os.IsNotExist(err) {
			return 0, err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return 0, err
		}
		p = parent
	}
}

// Executor performs the plan. Everything it removes is logged first.
type Executor struct {
	Policy Policy
	Log    io.Writer // append-only audit log
	Out    io.Writer // human output
	Now    func() time.Time
	Run    func(cmd string) error      // runs a shell command; injectable for tests
	Move   func(src, dst string) error // rename; injectable for tests
	Refs   func() ([]string, error)    // live process references, re-checked before each stale delete
	Q      *Quarantine                 // nil means permanent delete
}

func (x *Executor) logf(f string, a ...any) {
	if x.Log != nil {
		fmt.Fprintf(x.Log, "%s %s\n", x.Now().UTC().Format(time.RFC3339), fmt.Sprintf(f, a...))
	}
}

func (x *Executor) outf(f string, a ...any) {
	if x.Out != nil {
		fmt.Fprintf(x.Out, f, a...)
	}
}

// Execute runs the actionable items. It refuses the whole run, before touching
// anything, when the planned deletions exceed the per-run budget. Returns the
// bytes removed or quarantined by rm actions (commands report their own).
func (x *Executor) Execute(items []PlanItem) (int64, error) {
	var budget int64
	for _, it := range items {
		if it.Refused == nil && isDestructive(it.Action) {
			budget += it.Deletable
		}
	}
	maxBytes := int64(x.Policy.MaxDeleteGBPerRun * gb)
	if budget > maxBytes {
		return 0, refuse("budget", "plan would remove %.1f GB, policy max is %.1f GB per run; narrow with --types",
			float64(budget)/gb, x.Policy.MaxDeleteGBPerRun)
	}
	mode := "delete"
	if x.Q != nil {
		mode = "quarantine " + x.Q.Batch
	}
	x.logf("run start mode=%s budget=%d max=%d items=%d", mode, budget, maxBytes, len(items))
	var freed int64
	for _, it := range items {
		if it.Refused != nil {
			continue
		}
		switch it.Action {
		case ActionRmContents:
			n, err := x.rmContents(it.Path)
			freed += n
			if err != nil {
				x.logf("rm-contents %s partial freed=%d err=%v", it.Path, n, err)
				x.outf("  %s: partial, %s, error: %v\n", it.Path, human(n), err)
				continue
			}
			x.logf("rm-contents %s freed=%d", it.Path, n)
			x.outf("  %s: %s %s\n", it.Path, human(n), x.verb())
		case ActionRmStaleChilds:
			n, kept, err := x.rmStale(it)
			freed += n
			if err != nil {
				x.logf("rm-stale-children %s partial freed=%d kept=%d err=%v", it.Path, n, kept, err)
				x.outf("  %s: partial, %s, %d kept, error: %v\n", it.Path, human(n), kept, err)
				continue
			}
			x.logf("rm-stale-children %s freed=%d kept=%d", it.Path, n, kept)
			x.outf("  %s: %s %s, %d children kept\n", it.Path, human(n), x.verb(), kept)
		case ActionRm:
			n, err := x.dispose(it.Path, it.Bytes)
			if err != nil {
				x.logf("rm %s err=%v", it.Path, err)
				x.outf("  %s: error: %v\n", it.Path, err)
				continue
			}
			freed += n
			x.logf("rm %s freed=%d", it.Path, n)
			x.outf("  %s: %s %s\n", it.Path, human(n), x.verb())
		case ActionCommand:
			if !x.Policy.AllowCommands {
				x.logf("command %s skipped allow_commands=false", it.Path)
				x.outf("  %s: skipped, allow_commands is false\n", it.Path)
				continue
			}
			x.logf("command %s run=%q", it.Path, it.Command)
			x.outf("  %s: running %q\n", it.Path, it.Command)
			if err := x.Run(it.Command); err != nil {
				x.logf("command %s err=%v", it.Path, err)
				x.outf("  %s: command failed: %v\n", it.Path, err)
				continue
			}
		}
	}
	x.logf("run end freed=%d", freed)
	return freed, nil
}

func (x *Executor) verb() string {
	if x.Q != nil {
		return "quarantined"
	}
	return "removed"
}

// dispose removes one path: into quarantine when enabled, else permanently.
// Symlinks are moved or unlinked as links; targets are never touched.
func (x *Executor) dispose(path string, bytes int64) (int64, error) {
	if x.Q != nil {
		dst, err := x.Q.take(path, bytes, x.Now())
		if err != nil {
			return 0, err
		}
		x.logf("quarantine %s -> %s bytes=%d", path, dst, bytes)
		return bytes, nil
	}
	if fi, err := os.Lstat(path); err == nil && fi.IsDir() {
		makeWritable(path)
	}
	if err := os.RemoveAll(path); err != nil { // RemoveAll on a symlink removes the link only
		return 0, err
	}
	x.logf("remove %s bytes=%d", path, bytes)
	return bytes, nil
}

// rmContents disposes of every direct child of dir but keeps dir itself.
func (x *Executor) rmContents(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var freed int64
	var firstErr error
	for _, de := range entries {
		child := filepath.Join(dir, de.Name())
		fi, err := os.Lstat(child)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var size int64
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			size = 0
		case fi.IsDir():
			size, _ = pathSize(child)
		default:
			size = allocated(fi)
		}
		n, err := x.dispose(child, size)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			x.logf("rm-contents %s child=%s err=%v", dir, child, err)
			continue
		}
		freed += n
	}
	return freed, firstErr
}

// rmStale disposes of the children the plan marked for deletion, re-checking
// process references immediately before each one. A child that became
// referenced since the plan was built is kept.
func (x *Executor) rmStale(it PlanItem) (freed int64, kept int, err error) {
	if x.Refs == nil {
		return 0, 0, errors.New("no reference lister; refusing to remove stale children blind")
	}
	refs, err := x.Refs()
	if err != nil {
		return 0, 0, fmt.Errorf("re-check references: %w", err)
	}
	var firstErr error
	for _, c := range it.Children {
		if c.Keep != "" {
			kept++
			continue
		}
		if referenced(c.Path, refs) {
			kept++
			x.logf("rm-stale-children %s child=%s kept=now-referenced", it.Path, c.Path)
			continue
		}
		n, derr := x.dispose(c.Path, c.Bytes)
		if derr != nil {
			if firstErr == nil {
				firstErr = derr
			}
			x.logf("rm-stale-children %s child=%s err=%v", it.Path, c.Path, derr)
			continue
		}
		freed += n
	}
	return freed, kept, firstErr
}

// makeWritable adds u+w to every dir and file under root, without following links.
func makeWritable(root string) {
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		fi, err := d.Info()
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if fi.Mode().Perm()&0o200 == 0 {
			_ = os.Chmod(p, fi.Mode().Perm()|0o200)
		}
		return nil
	})
}

func shellRun(cmd string) error {
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func human(b int64) string {
	const (
		kb = 1024
		mb = kb * 1024
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/gb)
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/mb)
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/kb)
	}
	return fmt.Sprintf("%d B", b)
}

func humanDelta(b int64) string {
	if b < 0 {
		return "-" + human(-b)
	}
	return "+" + human(b)
}
