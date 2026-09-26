package plan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/testutil"
)

func safetyPlan(home, root string, p config.Policy) []Item {
	cfg := &config.Config{Home: home, Policy: p, KnownDirs: []config.Entry{{Path: root, Action: config.ActionRmContents}}}
	return Build(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, nil, time.Now())
}

func TestRejectSymlinkAncestor(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "data", "valuable")
	testutil.Write(t, target, 4096)
	if err := os.Symlink(outside, filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "alias", "data")
	p := testutil.PolicyFor(home)
	items := safetyPlan(home, root, p)
	if items[0].Refused == nil {
		t.Fatal("symlink ancestor accepted")
	}
	x := Executor{Home: home, Policy: p, Now: time.Now}
	// A caller-supplied or old plan cannot bypass the execution guard.
	_, err := x.Execute([]Item{{Entry: config.Entry{Path: root, Action: config.ActionRmContents}}})
	if err == nil {
		t.Fatal("unsafe execution accepted")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
}

func TestRejectProtectedDescendant(t *testing.T) {
	for _, kind := range []string{"never_touch", "always_disallowed"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, "cache")
			target := filepath.Join(root, "nested", "keep", "valuable")
			testutil.Write(t, target, 4096)
			p := testutil.PolicyFor(home)
			if kind == "never_touch" {
				p.NeverTouch = []string{filepath.Dir(target)}
			} else {
				p.AlwaysDisallowed = []string{filepath.Dir(target)}
			}
			if safetyPlan(home, root, p)[0].Refused == nil {
				t.Fatal("protected descendant accepted")
			}
			x := Executor{Home: home, Policy: p, Now: time.Now}
			if _, err := x.Execute([]Item{{Entry: config.Entry{Path: root, Action: config.ActionRmContents}}}); err == nil {
				t.Fatal("execution accepted")
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFreshBudgetRejectsCachedGrowth(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "cache")
	file := filepath.Join(root, "data")
	testutil.Write(t, file, 4096)
	old := size.Active
	size.Active = size.OpenCacheWith(filepath.Join(home, "sizes.json"), time.Hour, 0)
	defer func() { size.Active = old }()
	if _, err := size.PathSize(root); err != nil {
		t.Fatal(err)
	}
	p := testutil.PolicyFor(home)
	p.MaxDeleteGBPerRun = 0.001
	items := safetyPlan(home, root, p)
	testutil.Write(t, file, 4<<20)
	fresh := safetyPlan(home, root, p)
	if fresh[0].Deletable < 4<<20 {
		t.Fatalf("cached deletion size: %d", fresh[0].Deletable)
	}
	x := Executor{Home: home, Policy: p, Now: time.Now}
	if _, err := x.Execute(items); err == nil {
		t.Fatal("stale plan bypassed budget")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
}

func TestQuarantineSameSecondPreservesBothBatches(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	dir := filepath.Join(home, "q")
	q1, err := OpenQuarantine(dir, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(home, "valuable")
	testutil.Write(t, src, 4096)
	if _, err = q1.take(src, 4096, now); err != nil {
		t.Fatal(err)
	}
	q2, err := OpenQuarantine(dir, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if q1.Batch == q2.Batch {
		t.Fatal("batch reused")
	}
	if bs, err := ListBatches(dir); err != nil || len(bs) != 2 {
		t.Fatalf("batches: %v %v", bs, err)
	}
	if err = q2.Discard(); err != nil {
		t.Fatal(err)
	}
	n, _, err := RestoreBatch(dir, q1.Batch, nil)
	if err != nil || n != 1 {
		t.Fatalf("restore %d: %v", n, err)
	}
	if _, err = os.Stat(src); err != nil {
		t.Fatal(err)
	}
}

func TestQuarantineIntentSurvivesInterruptedMove(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	dir := filepath.Join(home, "q")
	src := filepath.Join(home, "valuable")
	testutil.Write(t, src, 4096)
	var q *Quarantine
	var err error
	q, err = OpenQuarantine(dir, now, func(from, to string) error {
		m, e := readManifest(dir, q.Batch)
		if e != nil || len(m.Entries) != 1 || !m.Entries[0].Pending {
			t.Fatalf("intent not persisted before move: %+v %v", m, e)
		}
		if e = os.Rename(from, to); e != nil {
			return e
		}
		return errors.New("interrupted after rename")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.take(src, 4096, now); err == nil {
		t.Fatal("injected interruption ignored")
	}
	if bs, _ := ListBatches(dir); len(bs) != 1 || bs[0].Count != -1 {
		t.Fatalf("pending batch eligible for purge: %+v", bs)
	}
	if _, names, err := PurgeBatches(dir, 0, now.Add(time.Hour), false, false); err != nil || len(names) != 0 {
		t.Fatalf("pending batch purged: %v %v", names, err)
	}
	n, _, err := RestoreBatch(dir, q.Batch, nil)
	if err != nil || n != 1 {
		t.Fatalf("recovery %d: %v", n, err)
	}
	if _, err = os.Stat(src); err != nil {
		t.Fatal(err)
	}
}

func TestQuarantineJournalFailureDoesNotMove(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "valuable")
	testutil.Write(t, src, 4096)
	moved := false
	q, err := OpenQuarantine(filepath.Join(home, "q"), time.Now(), func(string, string) error { moved = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(q.manifestPath()); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(q.manifestPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = q.take(src, 4096, time.Now()); err == nil {
		t.Fatal("journal failure ignored")
	}
	if moved {
		t.Fatal("move happened before durable journal")
	}
	if _, err = os.Stat(src); err != nil {
		t.Fatal(err)
	}
}

func TestRestorePreservesUnrecordedData(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	q, err := OpenQuarantine(dir, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(q.batchDir(), "unrecorded", "valuable")
	testutil.Write(t, unknown, 4096)
	if _, _, err := RestoreBatch(dir, q.Batch, nil); err == nil {
		t.Fatal("unrecorded data not reported")
	}
	if err := q.Discard(); err == nil {
		t.Fatal("discard accepted unrecorded data")
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal(err)
	}
}

func TestStaleRefreshesReferencesForEveryChild(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "cache")
	for _, name := range []string{"a", "b"} {
		testutil.Write(t, filepath.Join(dir, name, "data"), 4096)
		testutil.AgeTree(t, filepath.Join(dir, name), 48*time.Hour)
	}
	cfg := &config.Config{Home: home, Policy: testutil.PolicyFor(home), KnownDirs: []config.Entry{{Path: dir, Action: config.ActionRmStaleChilds, StaleAfterHours: 24}}}
	items := Build(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, nil, time.Now())
	calls := 0
	x := Executor{Home: home, Policy: cfg.Policy, Now: time.Now, Refs: func() ([]string, error) {
		calls++
		if calls > 1 {
			return []string{filepath.Join(dir, "b", "data")}, nil
		}
		return nil, nil
	}}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("reference calls %d", calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); !os.IsNotExist(err) {
		t.Fatal("first child not removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "b", "data")); err != nil {
		t.Fatal("newly referenced child removed", err)
	}
}

func TestStaleRechecksAgeAndIdentity(t *testing.T) {
	for _, change := range []string{"age", "identity"} {
		t.Run(change, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, "cache")
			child := filepath.Join(dir, "old")
			testutil.Write(t, filepath.Join(child, "data"), 4096)
			testutil.AgeTree(t, child, 48*time.Hour)
			cfg := &config.Config{Home: home, Policy: testutil.PolicyFor(home), KnownDirs: []config.Entry{{Path: dir, Action: config.ActionRmStaleChilds, StaleAfterHours: 24}}}
			items := Build(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, nil, time.Now())
			if change == "age" {
				testutil.AgeTree(t, child, time.Minute)
			} else {
				if err := os.Rename(child, filepath.Join(home, "original")); err != nil {
					t.Fatal(err)
				}
				testutil.Write(t, filepath.Join(child, "valuable"), 4096)
				testutil.AgeTree(t, child, 48*time.Hour)
			}
			x := Executor{Home: home, Policy: cfg.Policy, Now: time.Now, Refs: testutil.NoProcs}
			if _, err := x.Execute(items); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(child); err != nil {
				t.Fatal("changed child removed", err)
			}
		})
	}
}

func TestEnsureOpensLogBeforePurging(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	p := quarantinePolicy(home)
	src := filepath.Join(home, "valuable")
	testutil.Write(t, src, 4096)
	q, err := OpenQuarantine(p.QuarantineDir, now.Add(-30*24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.take(src, 4096, now); err != nil {
		t.Fatal(err)
	}
	p.LogFile = home // a directory cannot be opened as an append-only log
	du, err := size.Disk(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Home: home, Volume: home, Policy: p}
	if _, err = Ensure(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, du.FreeGB()+100, nil, true, now); err == nil {
		t.Fatal("unwritable log accepted")
	}
	if _, err = os.Stat(q.dest(src)); err != nil {
		t.Fatal("purged before opening log", err)
	}
}

func TestPurgePreservesUnrecordedDataUnlessExplicit(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	now := time.Now()
	q, err := OpenQuarantine(dir, now.Add(-time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(q.batchDir(), "unrecorded")
	testutil.Write(t, unknown, 4096)
	if _, names, err := PurgeBatches(dir, 0, now, false, false); err != nil || len(names) != 0 {
		t.Fatalf("auto-purge: %v %v", names, err)
	}
	if _, names, err := PurgeBatches(dir, 0, now, true, false); err != nil || len(names) != 0 {
		t.Fatalf("purge-all without include-held: %v %v", names, err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal(err)
	}
	if _, names, err := PurgeBatches(dir, 0, now, true, true); err != nil || len(names) != 1 {
		t.Fatalf("explicit purge: %v %v", names, err)
	}
}

func TestPurgeHonorsEnsureBudget(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	dir := filepath.Join(home, "q")
	q, err := OpenQuarantine(dir, now.Add(-time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(home, "large")
	testutil.Write(t, src, 4<<20)
	if _, err = q.take(src, 4<<20, now); err != nil {
		t.Fatal(err)
	}
	remaining := int64(1 << 20)
	if _, names, err := purgeBatches(dir, 0, now, false, &remaining); err != nil || len(names) != 0 {
		t.Fatalf("over-budget purge: %v %v", names, err)
	}
	if _, err := os.Stat(q.dest(src)); err != nil {
		t.Fatal(err)
	}
}

type brokenAudit struct{}

func (brokenAudit) Write([]byte) (int, error) { return 0, errors.New("audit device full") }

func TestAuditWriteFailureStopsDeletion(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "cache")
	file := filepath.Join(dir, "valuable")
	testutil.Write(t, file, 4096)
	p := testutil.PolicyFor(home)
	items := safetyPlan(home, dir, p)
	x := Executor{Home: home, Policy: p, Now: time.Now, Log: brokenAudit{}}
	if _, err := x.Execute(items); err == nil {
		t.Fatal("failed audit accepted")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
}
