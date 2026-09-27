package plan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/mutation"
	"github.com/afterdarksys/oos/internal/reserve"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	// Hermetic and fast: a small reserve, and no multi-second recovery poll.
	reserve.Size = 64 << 10
	recoveryPollWindow, recoveryPollInterval = 200*time.Millisecond, 50*time.Millisecond
	os.Exit(m.Run())
}

// enospcWriter fails its first n writes (n < 0: every write) like a full disk.
type enospcWriter struct {
	n   int
	buf bytes.Buffer
}

func (w *enospcWriter) Write(b []byte) (int, error) {
	if w.n != 0 {
		if w.n > 0 {
			w.n--
		}
		return 0, &os.PathError{Op: "write", Path: "log", Err: syscall.ENOSPC}
	}
	return w.buf.Write(b)
}

func withReserve(t *testing.T, p config.Policy) string {
	t.Helper()
	old := reserve.MinFree
	reserve.MinFree = 0
	defer func() { reserve.MinFree = old }()
	path := reserve.Path(p.StateFile)
	if err := reserve.Ensure(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestENOSPCPermanentDeleteReleasesReserveThenAuditsToStderr(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 4096)
	p := testutil.PolicyFor(home)
	res := withReserve(t, p)
	w := &enospcWriter{n: -1}
	var stderr bytes.Buffer
	x := &Executor{Policy: p, Log: w, Stderr: &stderr, Now: time.Now, Refs: testutil.NoProcs}
	if _, err := x.Execute([]Item{{Entry: config.Entry{Path: dir, Action: config.ActionRm}, Deletable: 4096}}); err != nil {
		t.Fatalf("permanent delete refused on a full disk: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("entry not deleted")
	}
	if _, err := os.Lstat(res); !os.IsNotExist(err) {
		t.Fatal("reserve not released on ENOSPC")
	}
	if !strings.Contains(stderr.String(), "audit log unavailable: ENOSPC; audit to stderr") || !strings.Contains(stderr.String(), "dispose intent path="+strconv.Quote(dir)) {
		t.Fatalf("audit not on stderr:\n%s", stderr.String())
	}
}

func TestENOSPCRetryAfterReserveRelease(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 4096)
	p := testutil.PolicyFor(home)
	withReserve(t, p)
	w := &enospcWriter{n: 1}
	var stderr bytes.Buffer
	x := &Executor{Policy: p, Log: w, Stderr: &stderr, Now: time.Now, Refs: testutil.NoProcs}
	if _, err := x.Execute([]Item{{Entry: config.Entry{Path: dir, Action: config.ActionRm}, Deletable: 4096}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.buf.String(), "run start") || stderr.Len() != 0 {
		t.Fatalf("retry after releasing the reserve should reach the log: log=%q stderr=%q", w.buf.String(), stderr.String())
	}
}

func TestENOSPCQuarantineStillRefuses(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 4096)
	p := quarantinePolicy(home)
	q, err := OpenQuarantine(p.QuarantineDir, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Discard()
	x := &Executor{Policy: p, Log: &enospcWriter{n: -1}, Stderr: &bytes.Buffer{}, Now: time.Now, Q: q, Refs: testutil.NoProcs}
	_, err = x.Execute([]Item{{Entry: config.Entry{Path: dir, Action: config.ActionRm}, Deletable: 4096}})
	if err == nil || !strings.Contains(err.Error(), "oos --ensure N -y") || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("quarantine without a journal must refuse actionably: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Fatal("moved without an audit line")
	}
}

func TestEnsureBuildDeadlineIsPartial(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 4096)
	old := ensureBuildShare
	ensureBuildShare = 1e-12
	defer func() { ensureBuildShare = old }()
	cfg := &config.Config{Home: home, Volume: home, Policy: testutil.PolicyFor(home),
		KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRm}}}
	du, _ := size.Disk(home)
	res, err := Ensure(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, du.FreeGB()+100, nil, false, time.Now())
	if !errors.Is(err, ErrPartial) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline not reported as partial: %v", err)
	}
	if !strings.Contains(strings.Join(res.Steps, "\n"), "plan-building phase ran out of time") {
		t.Fatalf("no step names the phase: %v", res.Steps)
	}
}

// A tombstone that cannot be removed must not stop Ensure from cleaning.
func TestEnsureContinuesPastStuckTombstone(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	p := quarantinePolicy(home)
	src := filepath.Join(home, "old")
	testutil.Write(t, src, 4096)
	q, err := OpenQuarantine(p.QuarantineDir, now.Add(-30*24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.take(src, 4096, now); err != nil {
		t.Fatal(err)
	}
	q.Discard()
	cache := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(cache, "f"), 8192)
	purgeCheckpoint = func(string) error { return errors.New("injected: tombstone stuck") }
	defer func() { purgeCheckpoint = nil }()
	cfg := &config.Config{Home: home, Volume: home, Policy: p,
		KnownDirs: []config.Entry{{Path: cache, Type: "cache", Action: config.ActionRmContents}}}
	du, _ := size.Disk(home)
	res, _ := Ensure(cfg, guard.Env{Home: home, Procs: testutil.NoProcs, Cwds: testutil.NoProcs}, du.FreeGB()+100, nil, true, now)
	steps := strings.Join(res.Steps, "\n")
	if !strings.Contains(steps, "quarantine purge incomplete") {
		t.Fatalf("purge failure not recorded: %v", res.Steps)
	}
	if _, err := os.Lstat(filepath.Join(cache, "f")); !os.IsNotExist(err) {
		t.Fatalf("ensure stopped at the tombstone: %v", res.Steps)
	}
	purgeCheckpoint = nil
	bs, _ := ListBatches(p.QuarantineDir)
	if len(bs) != 1 || !bs[0].Tombstone || !strings.HasPrefix(bs[0].Held, "purge incomplete: ") {
		t.Fatalf("stuck tombstone not listed as held: %+v", bs)
	}
}

func TestFailedIntentIsNotPersistedByNextTake(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	q, err := OpenQuarantine(dir, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer q.unlock()
	a, b := filepath.Join(home, "a"), filepath.Join(home, "b")
	testutil.Write(t, a, 10)
	testutil.Write(t, b, 10)
	old := manifestMaxBytes
	manifestMaxBytes = q.manSize + 10 // the empty manifest fits, one entry does not
	_, err = q.take(a, 10, time.Now())
	manifestMaxBytes = old
	if err == nil || len(q.man.Entries) != 0 {
		t.Fatalf("failed intent kept in memory: err=%v entries=%d", err, len(q.man.Entries))
	}
	if _, err = q.take(b, 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(dir, q.Batch)
	if err != nil || len(m.Entries) != 1 || m.Entries[0].From != b || m.Entries[0].Pending {
		t.Fatalf("manifest: %+v %v", m, err)
	}
	if _, err := os.Stat(a); err != nil {
		t.Fatal("source of the failed intent moved")
	}
}

// An intent that never ran (source is the recorded object, destination
// absent) holds nothing in the batch and must not pin it.
func TestPendingNeverMovedDoesNotPin(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	q, err := OpenQuarantine(dir, time.Now().Add(-time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer q.unlock()
	src := filepath.Join(home, "blob")
	testutil.Write(t, src, 10)
	id, err := identify(src)
	if err != nil {
		t.Fatal(err)
	}
	q.man.Entries = append(q.man.Entries, QEntry{From: src, To: q.dest(src), Bytes: 10, At: time.Now(), Pending: true, Identity: id})
	if err := q.writeManifest(); err != nil {
		t.Fatal(err)
	}
	if bs, _ := ListBatches(dir); len(bs) != 1 || bs[0].Held != "" {
		t.Fatalf("never-moved intent pinned the batch: %+v", bs)
	}
	testutil.Write(t, src, 11) // no longer provably the recorded object
	if bs, _ := ListBatches(dir); len(bs) != 1 || !strings.Contains(bs[0].Held, "pending") {
		t.Fatalf("unproven pending entry not held: %+v", bs)
	}
}

func TestBuildRecoversFromPanic(t *testing.T) {
	home := t.TempDir()
	old := planEntry
	planEntry = func(*config.Config, guard.Env, config.Entry, time.Time) Item { panic("boom") }
	defer func() { planEntry = old }()
	cfg := &config.Config{Home: home, Volume: home, Policy: testutil.PolicyFor(home),
		KnownDirs: []config.Entry{{Path: filepath.Join(home, "a", "b"), Type: "cache", Action: config.ActionRm}}}
	items := Build(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, nil, time.Now())
	if len(items) != 1 || items[0].Refused == nil || !strings.Contains(items[0].Refused.Error(), "panicked: boom") {
		t.Fatalf("panic not converted to a refusal: %+v", items)
	}
}

func manyFiles(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		testutil.Write(t, filepath.Join(dir, fmt.Sprintf("f%03d", i)), 1)
	}
}

// Every entry has its own budget, and removal is not charged for the measure.
func TestWorkBudgetIsPerEntryAndChargedOnce(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	p.ScanMaxEntries = 60 // one tree is ~51 entries; 3 walks of it are ~153
	var ents []config.Entry
	for _, n := range []string{"one", "two", "three"} {
		d := filepath.Join(home, "a", n)
		manyFiles(t, d, 50)
		ents = append(ents, config.Entry{Path: d, Type: "cache", Action: config.ActionRmContents})
	}
	cfg := &config.Config{Home: home, Volume: home, Policy: p, KnownDirs: ents}
	items := Build(cfg, guard.Env{Home: home, Procs: testutil.NoProcs, Cwds: testutil.NoProcs}, nil, time.Now())
	for _, it := range items {
		if it.Refused != nil {
			t.Fatalf("shared budget refused %s: %v", it.Path, it.Refused)
		}
	}
	x := &Executor{Policy: p, Now: time.Now, Refs: testutil.NoProcs}
	if _, err := x.Execute(items); err != nil {
		t.Fatalf("execute charged the budget more than once: %v", err)
	}
	big := filepath.Join(home, "a", "big")
	manyFiles(t, big, 80)
	_, err := x.Execute([]Item{{Entry: config.Entry{Path: big, Action: config.ActionRm}, Deletable: 80}})
	if err == nil || !strings.Contains(err.Error(), "policy.scan_max_entries") {
		t.Fatalf("over-budget entry refusal must name the setting: %v", err)
	}
}

func TestEnsureRecordsSkippedEntries(t *testing.T) {
	home := t.TempDir()
	keep := filepath.Join(home, "keep")
	testutil.Write(t, filepath.Join(keep, "f"), 10)
	cfg := &config.Config{Home: home, Volume: home, Policy: testutil.PolicyFor(home),
		KnownDirs: []config.Entry{
			{Path: keep, Type: "cache", Action: config.ActionRm},
			{Path: filepath.Join(home, "a", "gone"), Type: "cache", Action: config.ActionRm},
			{Path: filepath.Join(home, "a", "vm"), Type: "vm", Action: config.ActionCommand, Command: "true"},
		}}
	os.MkdirAll(filepath.Join(home, "a", "vm"), 0o755)
	du, _ := size.Disk(home)
	res, _ := Ensure(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, du.FreeGB()+100, nil, false, time.Now())
	steps := strings.Join(res.Steps, "\n")
	for _, want := range []string{"skip " + strconv.Quote(keep) + ": refused", "skip 1 entries whose path does not exist", "allow_commands is false"} {
		if !strings.Contains(steps, want) {
			t.Errorf("missing step %q in:\n%s", want, steps)
		}
	}
}

func TestSameVolume(t *testing.T) {
	home := t.TempDir()
	if !sameVolume(filepath.Join(home, "missing", "x"), home) {
		t.Fatal("a path and its volume must match")
	}
}

func TestPlanRefusesCommandsWhenDisallowed(t *testing.T) {
	home := t.TempDir()
	d := filepath.Join(home, "a", "vm")
	os.MkdirAll(d, 0o755)
	cfg := &config.Config{Home: home, Volume: home, Policy: testutil.PolicyFor(home),
		KnownDirs: []config.Entry{{Path: d, Type: "vm", Action: config.ActionCommand, Command: "true"}}}
	items := Build(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, nil, time.Now())
	if len(items) != 1 || items[0].Refused == nil || !strings.Contains(items[0].Refused.Error(), "allow_commands is false") {
		t.Fatalf("plan must show why the command will not run: %+v", items)
	}
}

func TestBatchesRollOverAndStayRestorable(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	for i := 0; i < 7; i++ {
		testutil.Write(t, filepath.Join(dir, fmt.Sprintf("c%d", i)), 10)
	}
	old := maxBatchEntries
	maxBatchEntries = 3
	defer func() { maxBatchEntries = old }()
	p := quarantinePolicy(home)
	cfg := &config.Config{Home: home, Volume: home, Policy: p}
	stores, err := OpenStores(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	x := &Executor{Policy: p, Now: time.Now, Stores: stores, Q: stores.Primary(), Refs: testutil.NoProcs}
	if _, err := x.Execute([]Item{{Entry: config.Entry{Path: dir, Action: config.ActionRmContents}, Deletable: 70}}); err != nil {
		t.Fatal(err)
	}
	bs := stores.Batches()
	if err := stores.DiscardEmpty(); err != nil {
		t.Fatal(err)
	}
	if len(bs) != 3 {
		t.Fatalf("want 3 batches of at most 3 entries, got %+v", bs)
	}
	for _, b := range bs {
		if n, skipped, err := RestoreBatch(b.Directory, b.Batch, nil); err != nil || n == 0 || n > 3 || len(skipped) != 0 {
			t.Fatalf("restore %s: n=%d skipped=%v err=%v", b.Batch, n, skipped, err)
		}
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 7 {
		t.Fatalf("restored %d of 7", len(ents))
	}
}

func TestManifestCapEnforcedAtWrite(t *testing.T) {
	home := t.TempDir()
	q, err := OpenQuarantine(filepath.Join(home, "q"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer q.unlock()
	old := manifestMaxBytes
	manifestMaxBytes = 10
	defer func() { manifestMaxBytes = old }()
	if err := q.writeManifest(); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized manifest written: %v", err)
	}
}

func TestQuarantineRefusesNonUTF8(t *testing.T) {
	home := t.TempDir()
	q, err := OpenQuarantine(filepath.Join(home, "q"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer q.unlock()
	_, err = q.take(filepath.Join(home, "bad\xffname"), 1, time.Now())
	if err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("non-UTF-8 path accepted: %v", err)
	}
	if !q.Empty() {
		t.Fatal("refused path journaled")
	}
}

func TestImplausibleBatchTimeIsHeld(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	now := time.Now()
	for _, created := range []time.Time{now.Add(48 * time.Hour), time.Date(2019, 6, 1, 0, 0, 0, 0, time.Local)} {
		src := filepath.Join(home, "c", created.Format("20060102"))
		testutil.Write(t, src, 10)
		q, err := OpenQuarantine(dir, created, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.take(src, 10, now); err != nil {
			t.Fatal(err)
		}
		q.Discard()
	}
	bs, _ := ListBatches(dir)
	if len(bs) != 2 || bs[0].Held != ClockHold || bs[1].Held != ClockHold {
		t.Fatalf("implausible times not held: %+v", bs)
	}
	if _, names, _ := PurgeBatches(dir, 0, now, true, false); len(names) != 0 {
		t.Fatalf("clock-skewed batches purged: %v", names)
	}
	if _, names, err := PurgeBatches(dir, 0, now, true, true); err != nil || len(names) != 2 {
		t.Fatalf("--include-held must still purge them: %v %v", names, err)
	}
}

// Another process holding the store lock (a restore) stops purge and take.
func TestStoreLockExcludesOtherProcesses(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	src := filepath.Join(home, "blob")
	testutil.Write(t, src, 10)
	q, err := OpenQuarantine(dir, time.Now().Add(-time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.take(src, 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	q.Discard()
	// A separate open file description conflicts like another process.
	f, err := os.OpenFile(filepath.Join(dir, storeLockName), os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	old := storeLockWait
	storeLockWait = 100 * time.Millisecond
	defer func() { storeLockWait = old }()
	if _, _, err := PurgeBatches(dir, 0, time.Now(), true, true); !errors.Is(err, mutation.ErrBusy) {
		t.Fatalf("purge ran while the store was locked: %v", err)
	}
	if _, _, err := RestoreBatch(dir, q.Batch, nil); !errors.Is(err, mutation.ErrBusy) {
		t.Fatalf("restore ran while the store was locked: %v", err)
	}
	if _, err := OpenQuarantine(dir, time.Now(), nil); !errors.Is(err, mutation.ErrBusy) {
		t.Fatalf("take opened a batch while the store was locked: %v", err)
	}
	unix.Flock(int(f.Fd()), unix.LOCK_UN)
	if n, _, err := RestoreBatch(dir, q.Batch, nil); err != nil || n != 1 {
		t.Fatalf("restore after release: %d %v", n, err)
	}
}

func TestShellCancelKillsProcessGroup(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_ = ShellRunContext(ctx, "sleep 30 & echo $! > "+pidfile+"; sleep 30")
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancel did not stop the command")
	}
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	for deadline := time.Now().Add(5 * time.Second); ; {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("background child survived cancellation")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAuditLogQuotesPaths(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	forged := "x\n2001-01-01T00:00:00Z rm \"/etc\" freed=1"
	testutil.Write(t, filepath.Join(dir, forged), 10)
	var log bytes.Buffer
	x := &Executor{Policy: testutil.PolicyFor(home), Log: &log, Now: time.Now, Refs: testutil.NoProcs}
	if _, err := x.Execute([]Item{{Entry: config.Entry{Path: dir, Action: config.ActionRmContents}, Deletable: 10}}); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(log.String()), "\n") {
		if strings.HasPrefix(line, "2001-") {
			t.Fatalf("path forged an audit line:\n%s", log.String())
		}
	}
}

func TestEmptyBatchWithoutManifestIsScaffolding(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	name := time.Now().Add(-time.Hour).Format(BatchLayout)
	if err := os.MkdirAll(filepath.Join(dir, name, "Users"), 0o700); err != nil {
		t.Fatal(err)
	}
	bs, _ := ListBatches(dir)
	if len(bs) != 1 || bs[0].Count != 0 || bs[0].Held != "" {
		t.Fatalf("empty crash leftover held: %+v", bs)
	}
	if _, names, err := PurgeBatches(dir, 0, time.Now(), true, false); err != nil || len(names) != 1 {
		t.Fatalf("scaffolding not purged: %v %v", names, err)
	}
	testutil.Write(t, filepath.Join(dir, name, "data"), 1)
	if bs, _ := ListBatches(dir); len(bs) != 1 || bs[0].Count != -1 {
		t.Fatalf("batch dir with data but no manifest must stay held: %+v", bs)
	}
}

func TestExecutePartialWrapsErrPartial(t *testing.T) {
	home := t.TempDir()
	a, b := filepath.Join(home, "a", "one"), filepath.Join(home, "a", "two")
	testutil.Write(t, filepath.Join(a, "f"), 10)
	testutil.Write(t, filepath.Join(b, "f"), 10)
	// No reference lister: rm-contents refuses at execution, after rm ran.
	x := &Executor{Policy: testutil.PolicyFor(home), Now: time.Now}
	items := []Item{
		{Entry: config.Entry{Path: a, Action: config.ActionRm}, Deletable: 10},
		{Entry: config.Entry{Path: b, Action: config.ActionRmContents}, Deletable: 10},
	}
	_, err := x.Execute(items)
	var r *guard.Refusal
	if !errors.Is(err, ErrPartial) || !errors.As(err, &r) {
		t.Fatalf("partial run not marked, or refusal lost: %v", err)
	}
	if _, err := x.Execute(items[1:]); err == nil || errors.Is(err, ErrPartial) {
		t.Fatalf("a run that did nothing is not partial: %v", err)
	}
}
