package main

import (
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
	Bytes   int64
	Refused error // non-nil means oos will not act on this entry
}

// buildPlan sizes every candidate entry in parallel and runs the guards.
// Entries marked never are included so the report shows them, but refused.
func buildPlan(cfg *Config, env Env, types []string) []PlanItem {
	ents := cfg.entries(types)
	items := make([]PlanItem, len(ents))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, e := range ents {
		wg.Add(1)
		go func(i int, e Entry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			it := PlanItem{Entry: e}
			if b, err := pathSize(e.Path); err == nil {
				it.Bytes = b
			} else if !os.IsNotExist(err) {
				it.Refused = refuse("size", "%v", err)
			} else {
				it.Refused = refuse("missing", "path does not exist")
			}
			if it.Refused == nil {
				it.Refused = env.checkDeletable(cfg.Policy, e)
			}
			items[i] = it
		}(i, e)
	}
	wg.Wait()
	sort.SliceStable(items, func(a, b int) bool { return items[a].Bytes > items[b].Bytes })
	return items
}

// Executor performs the plan. Everything it deletes is logged first.
type Executor struct {
	Policy Policy
	Log    io.Writer // append-only audit log
	Out    io.Writer // human output
	Now    func() time.Time
	Run    func(cmd string) error // runs a shell command; injectable for tests
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
// bytes actually freed by rm actions (commands report their own).
func (x *Executor) Execute(items []PlanItem) (int64, error) {
	var budget int64
	for _, it := range items {
		if it.Refused == nil && (it.Action == ActionRmContents || it.Action == ActionRm) {
			budget += it.Bytes
		}
	}
	maxBytes := int64(x.Policy.MaxDeleteGBPerRun * gb)
	if budget > maxBytes {
		return 0, refuse("budget", "plan would delete %.1f GB, policy max is %.1f GB per run; narrow with --types",
			float64(budget)/gb, x.Policy.MaxDeleteGBPerRun)
	}
	x.logf("run start budget=%d max=%d items=%d", budget, maxBytes, len(items))
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
				x.outf("  %s: partial, %s freed, error: %v\n", it.Path, human(n), err)
				continue
			}
			x.logf("rm-contents %s freed=%d", it.Path, n)
			x.outf("  %s: %s freed\n", it.Path, human(n))
		case ActionRm:
			if err := os.Remove(it.Path); err != nil {
				x.logf("rm %s err=%v", it.Path, err)
				x.outf("  %s: error: %v\n", it.Path, err)
				continue
			}
			freed += it.Bytes
			x.logf("rm %s freed=%d", it.Path, it.Bytes)
			x.outf("  %s: %s freed\n", it.Path, human(it.Bytes))
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

// rmContents removes every direct child of dir but keeps dir itself.
// Symlink children are unlinked, never followed. Read-only trees (Go module
// caches) are made writable first so the removal does not stop halfway.
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
		if fi.Mode()&os.ModeSymlink != 0 {
			size = 0
		} else if fi.IsDir() {
			size, _ = pathSize(child)
			makeWritable(child)
		} else {
			size = allocated(fi)
		}
		if err := os.RemoveAll(child); err != nil { // RemoveAll on a symlink removes the link only
			if firstErr == nil {
				firstErr = err
			}
			x.logf("rm-contents %s child=%s err=%v", dir, child, err)
			continue
		}
		freed += size
		x.logf("rm-contents %s child=%s bytes=%d", dir, child, size)
	}
	return freed, firstErr
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
