package plan

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/testutil"
)

func cloneOrSkip(t *testing.T, src, dst string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("clone sharing is an APFS behaviour")
	}
	if out, err := exec.Command("cp", "-c", src, dst).CombinedOutput(); err != nil {
		t.Skipf("cp -c unavailable: %v %s", err, out)
	}
}

// TestReclaimableDefaultsToDeletable: with nothing shared, the promise and
// the recorded total are the same number.
func TestReclaimableDefaultsToDeletable(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 300<<10)
	old := UniqueFloor
	UniqueFloor = 0
	defer func() { UniqueFloor = old }()
	cfg := &config.Config{Policy: testutil.PolicyFor(home), KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmContents}}}
	items := Build(cfg, guard.Env{Home: home, Procs: testutil.NoProcs, Cwds: testutil.NoProcs}, nil, time.Now())
	if len(items) != 1 || items[0].Refused != nil {
		t.Fatalf("items: %+v", items)
	}
	if items[0].Reclaimable != items[0].Deletable || items[0].Deletable == 0 {
		t.Errorf("reclaimable=%d deletable=%d", items[0].Reclaimable, items[0].Deletable)
	}
}

// TestBuildReportsReclaimableUnderClones: a tree of clones records every
// copy but promises only what a removal gives back. This is the uv archive
// case: 1087 entries reported as 151 GB returned 14 GB on 2026-09-08.
func TestBuildReportsReclaimableUnderClones(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "wheel", "lib.so"), 400<<10)
	cloneOrSkip(t, filepath.Join(dir, "wheel", "lib.so"), filepath.Join(dir, "env1-lib.so"))
	cloneOrSkip(t, filepath.Join(dir, "wheel", "lib.so"), filepath.Join(dir, "env2-lib.so"))
	old := UniqueFloor
	UniqueFloor = 0
	defer func() { UniqueFloor = old }()
	cfg := &config.Config{Policy: testutil.PolicyFor(home), KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmContents}}}
	items := Build(cfg, guard.Env{Home: home, Procs: testutil.NoProcs, Cwds: testutil.NoProcs}, nil, time.Now())
	it := items[0]
	if it.Refused != nil || it.Deletable < 3*(400<<10) {
		t.Fatalf("item: %+v", it)
	}
	if it.Reclaimable >= it.Deletable || it.Reclaimable < 400<<10 {
		t.Errorf("reclaimable=%d should be about one copy, deletable=%d", it.Reclaimable, it.Deletable)
	}
}

// TestStaleReclaimableCountsSharedChildrenOnce: stale children that are
// clones of one another (or of a kept child) return less than their sum.
func TestStaleReclaimableCountsSharedChildrenOnce(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "archive")
	testutil.Write(t, filepath.Join(dir, "kept", "lib.so"), 400<<10)
	cloneOrSkip(t, filepath.Join(dir, "kept", "lib.so"), filepath.Join(dir, "stale1"))
	cloneOrSkip(t, filepath.Join(dir, "kept", "lib.so"), filepath.Join(dir, "stale2"))
	for _, c := range []string{"stale1", "stale2"} {
		testutil.Age(t, filepath.Join(dir, c), 48*time.Hour)
	}
	// kept stays fresh by mtime; the clones aged out
	old := UniqueFloor
	UniqueFloor = 0
	defer func() { UniqueFloor = old }()
	cfg := &config.Config{Policy: testutil.PolicyFor(home), KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmStaleChilds, StaleAfterHours: 6}}}
	items := Build(cfg, guard.Env{Home: home, Procs: testutil.NoProcs, Cwds: testutil.NoProcs}, nil, time.Now())
	it := items[0]
	if it.Refused != nil {
		t.Fatalf("refused: %v", it.Refused)
	}
	if it.Deletable < 2*(400<<10) {
		t.Fatalf("both clones should be stale: deletable=%d children=%+v", it.Deletable, it.Children)
	}
	// both stale children share blocks with the kept child, which stays, so
	// removing them returns almost nothing
	if it.Reclaimable >= 400<<10 {
		t.Errorf("reclaimable=%d should be near zero when the origin is kept, deletable=%d", it.Reclaimable, it.Deletable)
	}
}

// TestQuarantineDiscardRemovesEmptyBatch: a run that quarantined nothing
// (commands only, or every item refused at execute time) leaves no batch
// directory behind; a batch with entries is never discarded.
func TestQuarantineDiscardRemovesEmptyBatch(t *testing.T) {
	home := t.TempDir()
	p := quarantinePolicy(home)
	q, err := OpenQuarantine(p.QuarantineDir, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !q.Empty() {
		t.Fatal("fresh batch should be empty")
	}
	if err := q.Discard(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.QuarantineDir, q.Batch)); !os.IsNotExist(err) {
		t.Errorf("empty batch dir should be gone: %v", err)
	}
	if bs, _ := ListBatches(p.QuarantineDir); len(bs) != 0 {
		t.Errorf("no batches expected: %+v", bs)
	}

	f := filepath.Join(home, "a", "f")
	testutil.Write(t, f, 10)
	q2, _ := OpenQuarantine(p.QuarantineDir, time.Now().Add(time.Second), nil)
	if _, err := q2.take(f, 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	if q2.Empty() {
		t.Fatal("batch with an entry is not empty")
	}
	if err := q2.Discard(); err != nil {
		t.Fatal(err)
	}
	if bs, _ := ListBatches(p.QuarantineDir); len(bs) != 1 || bs[0].Count != 1 {
		t.Errorf("a batch with entries must survive Discard: %+v", bs)
	}
}
