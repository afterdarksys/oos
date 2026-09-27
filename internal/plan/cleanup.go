package plan

import (
	"context"
	"errors"
	"fmt"
	"github.com/afterdarksys/oos/internal/worklimit"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/protect"
	"github.com/afterdarksys/oos/internal/reserve"
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
	Accounting  *size.Accounting  `json:"accounting,omitempty"`
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
	ctx, cancel := context.WithTimeout(env.Context(), cfg.Policy.OperationTimeout())
	defer cancel()
	env.Ctx = ctx
	ents := cfg.EntriesTagged(types, tag)
	items := make([]Item, len(ents))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	volumeSlots := map[uint64]chan struct{}{}
	for _, e := range ents {
		dev, _ := deviceOfPath(e.Path)
		if volumeSlots[dev] == nil {
			volumeSlots[dev] = make(chan struct{}, cfg.Policy.Workers())
		}
	}
	for i, e := range ents {
		wg.Add(1)
		go func(i int, e config.Entry) {
			defer wg.Done()
			// A panic in one entry's walk refuses that entry instead of
			// killing the daemon or CLI mid-plan.
			defer func() {
				if r := recover(); r != nil {
					stack := debug.Stack()
					if len(stack) > 2048 {
						stack = stack[:2048]
					}
					items[i] = Item{Entry: e, Refused: guard.Refuse("panic", "planning %s panicked: %v\n%s", e.Path, r, stack)}
				}
			}()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				items[i] = Item{Entry: e, Refused: ctx.Err()}
				return
			}
			defer func() { <-sem }()
			dev, _ := deviceOfPath(e.Path)
			slot := volumeSlots[dev]
			// A disappearing/changing device is refused instead of waiting on nil.
			if slot == nil {
				items[i] = Item{Entry: e, Refused: fmt.Errorf("device changed during planning")}
				return
			}
			select {
			case slot <- struct{}{}:
			case <-ctx.Done():
				items[i] = Item{Entry: e, Refused: ctx.Err()}
				return
			}
			defer func() { <-slot }()
			// Each entry has its own work budget, so concurrent entries
			// cannot starve each other into nondeterministic refusals.
			ienv := env
			ienv.Ctx = worklimit.With(ctx, cfg.Policy.EntryBudget(), cfg.Policy.VerificationBudget())
			items[i] = planEntry(cfg, ienv, e, now)
		}(i, e)
	}
	wg.Wait()
	sort.SliceStable(items, func(a, b int) bool { return items[a].Bytes > items[b].Bytes })
	return items
}

// planEntry is planItem; replaced in package tests only.
var planEntry = planItem

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
	// Say so in the plan (dry-run and JSON), not only when a live run skips it.
	if it.Refused == nil && e.Action == config.ActionCommand && !cfg.Policy.AllowCommands {
		it.Refused = guard.Refuse("commands", "allow_commands is false")
	}
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
			it.Refused = guard.Refuse("references", "%v", referencesHint(err))
			it.Bytes, _ = size.PathSize(e.Path)
			return it
		}
		children, err := guard.ClassifyChildrenContext(env.Context(), e.Path, time.Duration(e.StaleAfterHours)*time.Hour, refs, now)
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
			b, err = safefs.MeasureContext(env.Context(), e.Path)
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
		roots := []string{it.Path}
		if it.Action == config.ActionRmStaleChilds {
			roots = nil
			for _, c := range it.Children {
				if c.Keep == "" {
					roots = append(roots, c.Path)
				}
			}
		}
		a, err := size.Account(env.Context(), nil, roots...)
		if err == nil {
			it.Accounting = &a
			it.Reclaimable = a.Upper
		} else {
			it.Reclaimable = it.Deletable
		}
	}

	// Quarantine moves with rename, which cannot cross devices. Refuse now,
	// visibly in the plan, rather than failing halfway through a live run.
	if it.Refused == nil && cfg.Policy.Quarantine && config.IsDestructive(e.Action) {
		if err := sameDevice(e.Path, cfg.Policy.QuarantineFor(e.Path)); err != nil {
			it.Refused = guard.Refuse("quarantine", "%v", err)
		} else if !utf8.ValidString(e.Path) {
			it.Refused = guard.Refuse("quarantine", "path %q is not valid UTF-8; the quarantine manifest cannot record it for restore (rename it, or use --permanent)", e.Path)
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
	Stores     *Stores
	Ctx        context.Context
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
	Stderr     io.Writer  // audit fallback when the log hits ENOSPC; nil is os.Stderr
	audit      *auditLog
}

// logf writes an audit line. Paths must be formatted with %q so a name with
// a newline cannot forge a line.
func (x *Executor) logf(f string, a ...any) {
	if x.Log == nil {
		return
	}
	if x.audit == nil {
		x.audit = &auditLog{w: x.Log, reserve: reserve.Path(x.Policy.StateFile), stderr: x.Stderr}
	}
	// Only a quarantine move needs its journal; a permanent delete may
	// proceed with the audit on stderr when the disk is full.
	x.audit.permanent = x.Q == nil && x.Stores == nil
	if err := x.audit.line(x.Now().UTC().Format(time.RFC3339)+" "+fmt.Sprintf(f, a...), false); err != nil {
		x.logErr = sentinelErr{err, ErrAudit}
	}
}

// ErrAudit matches a run stopped because the audit log could not be
// written (exit 6). ErrRefused matches a run that did nothing because what
// it would have done was refused (exit 2). Both keep the wrapped message.
var (
	ErrAudit   = errors.New("audit log unwritable")
	ErrRefused = errors.New("refused; nothing was done")
)

// sentinelErr tags err with a sentinel without changing its message.
type sentinelErr struct {
	error
	tag error
}

func (e sentinelErr) Unwrap() error        { return e.error }
func (e sentinelErr) Is(target error) bool { return target == e.tag }

// entryBudget gives one entry (or one verification walk) its own work budget.
func (x *Executor) entryBudget(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return worklimit.With(ctx, x.Policy.EntryBudget(), x.Policy.VerificationBudget())
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
	originalCtx := x.Ctx
	defer func() { x.Ctx = originalCtx }()
	ctx := x.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, x.Policy.OperationTimeout())
	defer cancel()
	x.Ctx = ctx
	if x.Now == nil {
		x.Now = time.Now
	}
	x.budgetUsed = 0
	if x.Log != nil {
		// Best effort: refill the ENOSPC reserve while there is room.
		_ = reserve.Ensure(reserve.Path(x.Policy.StateFile))
	}
	var budget int64
	for _, it := range items {
		if it.Refused == nil && config.IsDestructive(it.Action) {
			if err := guard.CheckRemovalPath(x.Policy, it.Path, x.Home); err != nil {
				return 0, err
			}
			if err := unchangedItem(it); err != nil {
				return 0, err
			}
			actual, err := removalSizeContext(x.entryBudget(ctx), it)
			if err != nil {
				return 0, fmt.Errorf("measure %q: %w", it.Path, err)
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
	done := 0 // items completed; with a later failure the run is partial
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return freed, partial(done > 0 || freed > 0, err)
		}
		if it.Refused != nil {
			continue
		}
		if err := unchangedItem(it); err != nil {
			return freed, partial(done > 0 || freed > 0, err)
		}
		if x.Env != nil {
			if err := x.Env.CheckDeletable(x.Policy, it.Entry); err != nil {
				return freed, partial(done > 0 || freed > 0, err)
			}
		}
		// Each entry gets its own work budget: removal walks count once
		// per entry, and verification measures use budgets of their own.
		x.Ctx = x.entryBudget(ctx)
		switch it.Action {
		case config.ActionRmContents:
			n, err := x.rmContents(it.Path)
			freed += n
			if err != nil {
				runErr = errors.Join(runErr, err)
				x.logf("rm-contents %q partial freed=%d err=%q", it.Path, n, err.Error())
				x.outf("  %s: partial, %s, error: %v\n", it.Path, size.Human(n), err)
				continue
			}
			done++
			x.logf("rm-contents %q freed=%d%s", it.Path, n, uninspected())
			x.outf("  %s: %s %s\n", it.Path, size.Human(n), x.verb())
		case config.ActionRmStaleChilds:
			n, kept, err := x.rmStale(it)
			freed += n
			if err != nil {
				runErr = errors.Join(runErr, err)
				x.logf("rm-stale-children %q partial freed=%d kept=%d err=%q", it.Path, n, kept, err.Error())
				x.outf("  %s: partial, %s, %d kept, error: %v\n", it.Path, size.Human(n), kept, err)
				continue
			}
			done++
			x.logf("rm-stale-children %q freed=%d kept=%d%s", it.Path, n, kept, uninspected())
			x.outf("  %s: %s %s, %d children kept\n", it.Path, size.Human(n), x.verb(), kept)
		case config.ActionRm:
			n, err := x.dispose(it.Path)
			if err != nil {
				runErr = errors.Join(runErr, err)
				x.logf("rm %q err=%q", it.Path, err.Error())
				x.outf("  %s: error: %v\n", it.Path, err)
				continue
			}
			done++
			freed += n
			x.logf("rm %q freed=%d", it.Path, n)
			x.outf("  %s: %s %s\n", it.Path, size.Human(n), x.verb())
		case config.ActionCommand:
			if prefix, ok := protect.Hit(it.Path, x.Home, x.Policy.AlwaysDisallowed); ok {
				x.logf("command %q refused always_disallowed %q", it.Path, prefix)
				x.outf("  %s: refused, %s is always disallowed\n", it.Path, prefix)
				continue
			}
			if prefix, ok := protect.CommandHits(it.Command, x.Home, guard.CommandProtected(x.Policy)); ok {
				x.logf("command %q refused mentions %q", it.Path, prefix)
				x.outf("  %s: refused, command mentions %s\n", it.Path, prefix)
				continue
			}
			if !x.Policy.AllowCommands {
				x.logf("command %q skipped allow_commands=false", it.Path)
				x.outf("  %s: skipped, allow_commands is false\n", it.Path)
				continue
			}
			x.logf("command %q run=%q", it.Path, it.Command)
			x.outf("  %s: running %q\n", it.Path, it.Command)
			if x.logErr != nil {
				return freed, partial(done > 0 || freed > 0, x.logErr)
			}
			run := x.Run
			if run == nil {
				run = func(cmd string) error { return ShellRunContext(ctx, cmd) }
			}
			if err := run(it.Command); err != nil {
				runErr = errors.Join(runErr, err)
				x.logf("command %q err=%q", it.Path, err.Error())
				x.outf("  %s: command failed: %v\n", it.Path, err)
				continue
			}
			done++
		}
	}
	x.logf("run end freed=%d", freed)
	return freed, partial(done > 0 || freed > 0, errors.Join(runErr, x.logErr))
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
	// The measure is verification, not removal: its own work budget, so a
	// tree is not charged twice against the entry's budget.
	bytes, err := safefs.MeasureContext(x.entryBudget(x.Ctx), path)
	if err != nil {
		return 0, err
	}
	if bytes > x.remaining {
		return 0, guard.Refuse("budget", "remaining budget exceeded by %q", path)
	}
	// Check active use after the potentially long measurement, just before acting.
	if check != nil {
		if err := check(); err != nil {
			return 0, err
		}
	}
	x.logf("dispose intent path=%q bytes=%d", path, bytes)
	if x.logErr != nil {
		return 0, x.logErr
	}
	if x.Q != nil {
		if err := x.Ctx.Err(); err != nil {
			return 0, err
		}
		q := x.Q
		if x.Stores != nil {
			var err error
			q, err = x.Stores.For(path)
			if err != nil {
				return 0, err
			}
		} else if q.full() {
			next, err := OpenQuarantine(q.Dir, x.Now(), q.move)
			if err != nil {
				return 0, err
			}
			next.Hash, next.checkpoint = q.Hash, q.checkpoint
			q.unlock()
			x.Q, q = next, next
			x.logf("quarantine batch full; continuing in %q", next.Batch)
		}
		// Reserve before the move: a journal/sync failure may occur after rename.
		x.remaining -= bytes
		q.Ctx = x.Ctx
		dst, err := q.take(path, bytes, x.Now())
		if err != nil {
			return 0, err
		}
		x.logf("quarantine %q -> %q bytes=%d", path, dst, bytes)
		return bytes, nil
	}
	n, err := safefs.RemoveContext(x.Ctx, path, &x.remaining)
	if err != nil {
		x.logf("remove %q bytes=%d err=%q", path, n, err.Error())
	} else {
		x.logf("remove %q bytes=%d err=<nil>", path, n)
	}
	return n, err
}

// removalSize never consults the observational size cache.
func removalSize(it Item) (int64, error) { return removalSizeContext(context.Background(), it) }
func removalSizeContext(ctx context.Context, it Item) (int64, error) {
	if it.Action != config.ActionRmStaleChilds {
		return safefs.MeasureContext(ctx, it.Path)
	}
	var n int64
	for _, c := range it.Children {
		if c.Keep != "" {
			continue
		}
		b, err := safefs.MeasureContext(ctx, c.Path)
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
	// Like rmStale, a child a running process uses (command line, cwd or open
	// file) is left in place. Without a reference list the entry is refused.
	if x.Refs == nil {
		return 0, guard.Refuse("references", "no reference lister; refusing to empty %s blind", dir)
	}
	refs, err := x.Refs()
	if err != nil {
		return 0, guard.Refuse("references", "cannot list process references for %q: %v", dir, referencesHint(err))
	}
	entries, err := safefs.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var freed int64
	var firstErr error
	var notUTF8 int
	for _, de := range entries {
		child := filepath.Join(dir, de.Name())
		if guard.Referenced(child, refs) {
			x.logf("rm-contents %q child=%q kept referenced by a running process", dir, child)
			continue
		}
		if x.Q != nil && !utf8.ValidString(child) {
			notUTF8++
			x.logf("rm-contents %q child=%q kept: not valid UTF-8, cannot be quarantined", dir, child)
			x.outf("  %q: kept, not valid UTF-8 (cannot be recorded for restore)\n", child)
			continue
		}
		n, err := x.dispose(child)
		freed += n
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			x.logf("rm-contents %q child=%q err=%q", dir, child, err.Error())
			return freed, firstErr
		}
	}
	if notUTF8 > 0 {
		firstErr = guard.Refuse("quarantine", "%d paths under %q are not valid UTF-8 and were left in place (rename them, or use --permanent)", notUTF8, dir)
	}
	return freed, firstErr
}

var errKeepChild = errors.New("stale child must be kept")

// referencesHint says what to do when the process listing cannot be trusted.
// The refusal itself stands: a path whose users cannot be seen is kept.
func referencesHint(err error) error {
	var u *guard.UnreadableProcessesError
	switch {
	case errors.Is(err, guard.ErrPIDNamespace):
		return fmt.Errorf("cannot verify processes using this path: running in a container PID namespace; run oos on the host: %w", err)
	case errors.As(err, &u):
		return fmt.Errorf("cannot verify processes using this path; run oos as root so every process can be inspected: %w", err)
	}
	return err
}

// uninspected annotates an audit line with processes of other users that a
// non-root listing could not read.
func uninspected() string {
	if n := guard.OtherUserProcessesNotInspected(); n > 0 {
		return fmt.Sprintf(" other_user_processes_not_inspected=%d", n)
	}
	return ""
}

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
				return fmt.Errorf("re-check references: %w", referencesHint(refErr))
			}
			fi, statErr := os.Lstat(c.Path)
			if os.IsNotExist(statErr) {
				return errKeepChild
			}
			if statErr != nil {
				return statErr
			}
			if fi.Mode()&os.ModeSymlink != 0 || (c.Info != nil && !os.SameFile(c.Info, fi)) || guard.Referenced(c.Path, refs) {
				return errKeepChild
			}
			// Same rule as the plan: newest change anywhere in the subtree,
			// and a walk that cannot finish keeps the child.
			newest, walkErr := guard.NewestChange(x.entryBudget(x.Ctx), c.Path)
			if walkErr != nil || x.Now().Sub(newest) < time.Duration(it.StaleAfterHours)*time.Hour {
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
			x.logf("rm-stale-children %q child=%q err=%q", it.Path, c.Path, derr.Error())
			return freed, kept, firstErr
		}
	}
	return freed, kept, firstErr
}

func ShellRun(cmd string) error {
	return ShellRunContext(context.Background(), cmd)
}

// shellWaitDelay bounds the wait for output after the group is killed.
var shellWaitDelay = 5 * time.Second

// ShellRunContext runs cmd in its own process group; cancelling ctx kills
// the whole group, so a command's children cannot outlive the run.
func ShellRunContext(ctx context.Context, cmd string) error {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	c.WaitDelay = shellWaitDelay
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
