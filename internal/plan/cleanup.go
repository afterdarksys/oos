package plan

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/protect"
	"github.com/afterdarksys/oos/internal/safefs"
	"github.com/afterdarksys/oos/internal/size"
)

// Item is one entry after sizing and guarding.
// UniqueFloor is the deletable size from which the plan measures what a
// removal would really give back. Under it the recorded and returned figures
// are taken as equal: the clone-aware walk opens every large file, and a
// small entry cannot be far wrong.
var UniqueFloor int64 = size.GB

type Item struct {
	config.Entry
	Bytes       int64             // total under the path
	Deletable   int64             // what a live run would remove, as recorded
	Reclaimable int64             // what the volume gets back; Deletable unless blocks are shared
	Children    []guard.ChildPlan // rm-stale-children only
	Info        os.FileInfo       `json:"-"` // identity at planning time
	Refused     error             // non-nil means oos will not act on this entry
}

// Build sizes every candidate entry in parallel and runs the guards.
// Entries marked never are included so the report shows them, but refused.
func Build(cfg *config.Config, env guard.Env, types []string, now time.Time) []Item {
	return BuildTagged(cfg, env, types, "", now)
}

// BuildTagged is buildPlan narrowed to entries carrying tag ("" = all).
func BuildTagged(cfg *config.Config, env guard.Env, types []string, tag string, now time.Time) []Item {
	ents := cfg.EntriesTagged(types, tag)
	items := make([]Item, len(ents))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, e := range ents {
		wg.Add(1)
		go func(i int, e config.Entry) {
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

func planItem(cfg *config.Config, env guard.Env, e config.Entry, now time.Time) Item {
	it := Item{Entry: e}
	fi, statErr := os.Lstat(e.Path)
	if err := statErr; err != nil {
		if os.IsNotExist(err) {
			it.Refused = guard.Refuse("missing", "path does not exist")
		} else {
			it.Refused = guard.Refuse("stat", "%v", err)
		}
		return it
	}
	it.Info = fi
	it.Refused = env.CheckDeletable(cfg.Policy, e)
	if it.Refused == nil && config.IsDestructive(e.Action) {
		for _, other := range cfg.Entries(nil) {
			if other.Action == config.ActionNever && config.IsUnder(other.Path, e.Path) {
				it.Refused = guard.Refuse("never_touch", "%s contains entry marked never: %s", e.Path, other.Path)
				break
			}
		}
	}

	switch e.Action {
	case config.ActionRmStaleChilds:
		if it.Refused != nil {
			it.Bytes, _ = size.PathSize(e.Path)
			return it
		}
		refs, err := env.References()
		if err != nil {
			it.Refused = guard.Refuse("references", "%v", err)
			it.Bytes, _ = size.PathSize(e.Path)
			return it
		}
		children, err := guard.ClassifyChildren(e.Path, time.Duration(e.StaleAfterHours)*time.Hour, refs, now)
		if err != nil {
			it.Refused = guard.Refuse("children", "%v", err)
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
		var b int64
		var err error
		if it.Refused == nil && config.IsDestructive(e.Action) {
			b, err = safefs.Measure(e.Path)
		} else {
			b, err = size.PathSize(e.Path)
		}
		if err != nil && it.Refused == nil {
			it.Refused = guard.Refuse("size", "%v", err)
		}
		it.Bytes = b
		if it.Refused == nil && (e.Action == config.ActionRm || e.Action == config.ActionRmContents) {
			it.Deletable = b
		}
	}

	if it.Refused == nil && config.IsDestructive(e.Action) {
		it.Reclaimable = reclaimable(it, now)
	}

	// Quarantine moves with rename, which cannot cross devices. Refuse now,
	// visibly in the plan, rather than failing halfway through a live run.
	if it.Refused == nil && cfg.Policy.Quarantine && config.IsDestructive(e.Action) {
		if err := sameDevice(e.Path, cfg.Policy.QuarantineDir); err != nil {
			it.Refused = guard.Refuse("quarantine", "%v", err)
		}
	}
	return it
}

// reclaimable is what removing the item returns to the volume. Deletable is
// the recorded figure, allocated blocks summed per file; where files share
// blocks (APFS clones, hardlinks) the volume gives back less. The uv archive
// on 2026-09-08 recorded 151 GB and returned 14. The measurement is a walk
// that opens every large file, so it runs only from UniqueFloor up and is
// cached against the deletable total it was taken for.
func reclaimable(it Item, now time.Time) int64 {
	if it.Deletable < UniqueFloor {
		return it.Deletable
	}
	roots := []string{it.Path}
	var keep []string
	key := it.Path
	if it.Action == config.ActionRmStaleChilds {
		// the stale set can change with its total unchanged (two children
		// of one size swapping state), so the set itself is in the key
		roots = roots[:0]
		h := fnv.New64a()
		for _, c := range it.Children {
			if c.Keep == "" {
				roots = append(roots, c.Path)
				h.Write([]byte(c.Path))
				h.Write([]byte{0})
			} else {
				keep = append(keep, c.Path)
			}
		}
		key += fmt.Sprintf("#stale:%x", h.Sum64())
	}
	u, ok := size.Active.Unique(key, it.Deletable, now, func() (int64, error) {
		_, u, err := size.UniqueAgainst(keep, roots...)
		return u, err
	})
	if !ok || u > it.Deletable {
		return it.Deletable
	}
	return u
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
			dev, ok := size.DeviceOf(fi)
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
	Policy     config.Policy
	Log        io.Writer // append-only audit log
	Out        io.Writer // human output
	Now        func() time.Time
	Run        func(cmd string) error      // runs a shell command; injectable for tests
	Move       func(src, dst string) error // rename; injectable for tests
	Refs       func() ([]string, error)    // live process references, re-checked before each stale delete
	Q          *Quarantine                 // nil means permanent delete
	remaining  int64
	budgetUsed int64
	logErr     error
	Env        *guard.Env // live guard rechecks; optional for injected executors
	Home       string     // expands ~/.ssh and the other home-relative always_disallowed entries
}

func (x *Executor) logf(f string, a ...any) {
	if x.Log != nil {
		_, err := fmt.Fprintf(x.Log, "%s %s\n", x.Now().UTC().Format(time.RFC3339), fmt.Sprintf(f, a...))
		if err != nil {
			x.logErr = err
		}
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
func (x *Executor) Execute(items []Item) (int64, error) {
	if x.Now == nil {
		x.Now = time.Now
	}
	x.budgetUsed = 0
	var budget int64
	for _, it := range items {
		if it.Refused == nil && config.IsDestructive(it.Action) {
			if err := guard.CheckRemovalPath(x.Policy, it.Path, x.Home); err != nil {
				return 0, err
			}
			if err := unchangedItem(it); err != nil {
				return 0, err
			}
			actual, err := removalSize(it)
			if err != nil {
				return 0, err
			}
			if actual < it.Deletable {
				actual = it.Deletable
			}
			budget += actual
		}
	}
	maxBytes := int64(x.Policy.MaxDeleteGBPerRun * size.GB)
	if budget > maxBytes {
		return 0, guard.Refuse("budget", "plan would remove %.1f GB, policy max is %.1f GB per run; narrow with --types",
			float64(budget)/size.GB, x.Policy.MaxDeleteGBPerRun)
	}
	x.remaining = maxBytes
	defer func() { x.budgetUsed = maxBytes - x.remaining }()
	mode := "delete"
	if x.Q != nil {
		mode = "quarantine " + x.Q.Batch
	}
	if why, ok := protect.StagedInstall(); ok {
		return 0, guard.Refuse("install", "macOS upgrade payload at %s; refusing to change the disk until that directory is gone", why)
	}
	x.logf("run start mode=%s budget=%d max=%d items=%d", mode, budget, maxBytes, len(items))
	if x.logErr != nil {
		return 0, x.logErr
	}
	var runErr error
	var freed int64
	for _, it := range items {
		if it.Refused != nil {
			continue
		}
		if err := unchangedItem(it); err != nil {
			return freed, err
		}
		if x.Env != nil {
			if err := x.Env.CheckDeletable(x.Policy, it.Entry); err != nil {
				return freed, err
			}
		}
		switch it.Action {
		case config.ActionRmContents:
			n, err := x.rmContents(it.Path)
			freed += n
			if err != nil {
				runErr = errors.Join(runErr, err)
				x.logf("rm-contents %s partial freed=%d err=%v", it.Path, n, err)
				x.outf("  %s: partial, %s, error: %v\n", it.Path, size.Human(n), err)
				continue
			}
			x.logf("rm-contents %s freed=%d", it.Path, n)
			x.outf("  %s: %s %s\n", it.Path, size.Human(n), x.verb())
		case config.ActionRmStaleChilds:
			n, kept, err := x.rmStale(it)
			freed += n
			if err != nil {
				runErr = errors.Join(runErr, err)
				x.logf("rm-stale-children %s partial freed=%d kept=%d err=%v", it.Path, n, kept, err)
				x.outf("  %s: partial, %s, %d kept, error: %v\n", it.Path, size.Human(n), kept, err)
				continue
			}
			x.logf("rm-stale-children %s freed=%d kept=%d", it.Path, n, kept)
			x.outf("  %s: %s %s, %d children kept\n", it.Path, size.Human(n), x.verb(), kept)
		case config.ActionRm:
			n, err := x.dispose(it.Path)
			if err != nil {
				runErr = errors.Join(runErr, err)
				x.logf("rm %s err=%v", it.Path, err)
				x.outf("  %s: error: %v\n", it.Path, err)
				continue
			}
			freed += n
			x.logf("rm %s freed=%d", it.Path, n)
			x.outf("  %s: %s %s\n", it.Path, size.Human(n), x.verb())
		case config.ActionCommand:
			if prefix, ok := protect.Hit(it.Path, x.Home, x.Policy.AlwaysDisallowed); ok {
				x.logf("command %s refused always_disallowed %s", it.Path, prefix)
				x.outf("  %s: refused, %s is always disallowed\n", it.Path, prefix)
				continue
			}
			if prefix, ok := protect.CommandHits(it.Command, x.Home, x.Policy.AlwaysDisallowed); ok {
				x.logf("command %s refused mentions %s", it.Path, prefix)
				x.outf("  %s: refused, command mentions %s\n", it.Path, prefix)
				continue
			}
			if !x.Policy.AllowCommands {
				x.logf("command %s skipped allow_commands=false", it.Path)
				x.outf("  %s: skipped, allow_commands is false\n", it.Path)
				continue
			}
			x.logf("command %s run=%q", it.Path, it.Command)
			x.outf("  %s: running %q\n", it.Path, it.Command)
			if x.logErr != nil {
				return freed, x.logErr
			}
			if err := x.Run(it.Command); err != nil {
				runErr = errors.Join(runErr, err)
				x.logf("command %s err=%v", it.Path, err)
				x.outf("  %s: command failed: %v\n", it.Path, err)
				continue
			}
		}
	}
	x.logf("run end freed=%d", freed)
	return freed, errors.Join(runErr, x.logErr)
}

func (x *Executor) verb() string {
	if x.Q != nil {
		return "quarantined"
	}
	return "removed"
}

// dispose removes one path: into quarantine when enabled, else permanently.
// Symlinks are moved or unlinked as links; targets are never touched.
func (x *Executor) dispose(path string) (int64, error) {
	return x.disposeChecked(path, nil)
}

func (x *Executor) disposeChecked(path string, check func() error) (int64, error) {
	if err := guard.CheckRemovalPath(x.Policy, path, x.Home); err != nil {
		return 0, err
	}
	bytes, err := safefs.Measure(path)
	if err != nil {
		return 0, err
	}
	if bytes > x.remaining {
		return 0, guard.Refuse("budget", "remaining budget exceeded by %s", path)
	}
	// Check active use after the potentially long measurement, just before acting.
	if check != nil {
		if err := check(); err != nil {
			return 0, err
		}
	}
	x.logf("dispose intent path=%s bytes=%d", path, bytes)
	if x.logErr != nil {
		return 0, x.logErr
	}
	if x.Q != nil {
		// Reserve before the move: a journal/sync failure may occur after rename.
		x.remaining -= bytes
		dst, err := x.Q.take(path, bytes, x.Now())
		if err != nil {
			return 0, err
		}
		x.logf("quarantine %s -> %s bytes=%d", path, dst, bytes)
		return bytes, nil
	}
	n, err := safefs.Remove(path, &x.remaining)
	x.logf("remove %s bytes=%d err=%v", path, n, err)
	return n, err
}

// removalSize never consults the observational size cache.
func removalSize(it Item) (int64, error) {
	if it.Action != config.ActionRmStaleChilds {
		return safefs.Measure(it.Path)
	}
	var n int64
	for _, c := range it.Children {
		if c.Keep != "" {
			continue
		}
		b, err := safefs.Measure(c.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		n += b
	}
	return n, nil
}

// rmContents disposes of every direct child of dir but keeps dir itself.
func (x *Executor) rmContents(dir string) (int64, error) {
	if prefix, ok := protect.Hit(dir, x.Home, x.Policy.AlwaysDisallowed); ok {
		return 0, guard.Refuse("always_disallowed", "refusing %s: %s cannot be removed", dir, prefix)
	}
	entries, err := safefs.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var freed int64
	var firstErr error
	for _, de := range entries {
		child := filepath.Join(dir, de.Name())
		n, err := x.dispose(child)
		freed += n
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			x.logf("rm-contents %s child=%s err=%v", dir, child, err)
			return freed, firstErr
		}
	}
	return freed, firstErr
}

var errKeepChild = errors.New("stale child must be kept")

// rmStale disposes of the children the plan marked for deletion, re-checking
// process references immediately before each one. A child that became
// referenced since the plan was built is kept.
func (x *Executor) rmStale(it Item) (freed int64, kept int, err error) {
	if x.Refs == nil {
		return 0, 0, errors.New("no reference lister; refusing to remove stale children blind")
	}
	var firstErr error
	for _, c := range it.Children {
		if c.Keep != "" {
			kept++
			continue
		}
		n, derr := x.disposeChecked(c.Path, func() error {
			refs, refErr := x.Refs()
			if refErr != nil {
				return fmt.Errorf("re-check references: %w", refErr)
			}
			fi, statErr := os.Lstat(c.Path)
			if os.IsNotExist(statErr) {
				return errKeepChild
			}
			if statErr != nil {
				return statErr
			}
			if fi.Mode()&os.ModeSymlink != 0 || (c.Info != nil && !os.SameFile(c.Info, fi)) ||
				x.Now().Sub(fi.ModTime()) < time.Duration(it.StaleAfterHours)*time.Hour || guard.Referenced(c.Path, refs) {
				return errKeepChild
			}
			return nil
		})
		if errors.Is(derr, errKeepChild) || os.IsNotExist(derr) {
			kept++
			continue
		}
		freed += n
		if derr != nil {
			if firstErr == nil {
				firstErr = derr
			}
			x.logf("rm-stale-children %s child=%s err=%v", it.Path, c.Path, derr)
			return freed, kept, firstErr
		}
	}
	return freed, kept, firstErr
}

func ShellRun(cmd string) error {
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func unchangedItem(it Item) error {
	if it.Info == nil {
		return nil
	}
	current, err := os.Lstat(it.Path)
	if err != nil {
		return err
	}
	if !os.SameFile(it.Info, current) {
		return guard.Refuse("changed", "%s changed since planning", it.Path)
	}
	return nil
}
