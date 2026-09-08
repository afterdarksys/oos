package plan

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/testutil"
)

func quarantinePolicy(home string) config.Policy {
	p := testutil.PolicyFor(home)
	p.Quarantine = true
	p.QuarantineDir = filepath.Join(home, "q")
	p.QuarantineDays = 7
	return p
}

func staleFixture(t *testing.T) (home, dir string) {
	t.Helper()
	home = t.TempDir()
	dir = filepath.Join(home, "a", "archive")
	testutil.Write(t, filepath.Join(dir, "oldunref", "f"), 4096)
	testutil.Age(t, filepath.Join(dir, "oldunref"), 48*time.Hour)
	testutil.Write(t, filepath.Join(dir, "oldref", "bin", "python"), 4096)
	testutil.Age(t, filepath.Join(dir, "oldref"), 48*time.Hour)
	testutil.Write(t, filepath.Join(dir, "fresh", "f"), 4096)
	testutil.Write(t, filepath.Join(dir, "cwdref", "f"), 4096)
	testutil.Age(t, filepath.Join(dir, "cwdref"), 48*time.Hour)
	testutil.Write(t, filepath.Join(dir, ".lock"), 1)
	testutil.Age(t, filepath.Join(dir, ".lock"), 48*time.Hour)
	if err := os.Symlink(filepath.Join(home, "a"), filepath.Join(dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	return home, dir
}

func TestStalePlanAndExecuteDeletesOnlyStaleUnreferenced(t *testing.T) {
	home, dir := staleFixture(t)
	ref := "/usr/local/bin/uv tool uvx " + filepath.Join(dir, "oldref", "bin", "python")
	env := guard.Env{Home: home,
		Procs: func() ([]string, error) { return []string{ref}, nil },
		Cwds:  func() ([]string, error) { return []string{filepath.Join(dir, "cwdref")}, nil }}
	cfg := &config.Config{Version: 1, Volume: home, Policy: testutil.PolicyFor(home), Home: home,
		KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmStaleChilds, StaleAfterHours: 24}}}
	items := Build(cfg, env, nil, time.Now())
	if len(items) != 1 || items[0].Refused != nil {
		t.Fatalf("plan: %+v", items)
	}
	if items[0].Deletable <= 0 || items[0].Deletable >= items[0].Bytes {
		t.Fatalf("deletable %d should be a strict subset of %d", items[0].Deletable, items[0].Bytes)
	}
	var log bytes.Buffer
	x := &Executor{Policy: cfg.Policy, Log: &log, Now: time.Now, Refs: env.References}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "oldunref")); err == nil {
		t.Error("oldunref should be gone")
	}
	for _, keep := range []string{"oldref", "fresh", "cwdref", ".lock", "lnk"} {
		if _, err := os.Lstat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s should survive", keep)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "a", "archive")); err != nil {
		t.Error("the directory itself must survive")
	}
}

func TestStaleRecheckAtExecuteTime(t *testing.T) {
	home, dir := staleFixture(t)
	empty := func() ([]string, error) { return nil, nil }
	env := guard.Env{Home: home, Procs: empty, Cwds: empty}
	cfg := &config.Config{Version: 1, Volume: home, Policy: testutil.PolicyFor(home), Home: home,
		KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmStaleChilds, StaleAfterHours: 24}}}
	items := Build(cfg, env, nil, time.Now())
	// Between plan and execute, oldunref becomes referenced.
	late := func() ([]string, error) { return []string{"python " + filepath.Join(dir, "oldunref") + "/f"}, nil }
	x := &Executor{Policy: cfg.Policy, Now: time.Now, Refs: late}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "oldunref")); err != nil {
		t.Error("a child referenced at execute time must be kept")
	}
	// And with no reference lister at all, the item is refused, not deleted:
	// oldunref is still a delete candidate in the plan and must survive.
	x = &Executor{Policy: cfg.Policy, Now: time.Now}
	_, _ = x.Execute(items)
	if _, err := os.Stat(filepath.Join(dir, "oldunref")); err != nil {
		t.Error("blind execute must not delete")
	}
}

func TestStaleFailsClosedWhenReferencesUnavailable(t *testing.T) {
	home, dir := staleFixture(t)
	env := guard.Env{Home: home, Procs: func() ([]string, error) { return nil, errors.New("ps broke") }}
	cfg := &config.Config{Version: 1, Volume: home, Policy: testutil.PolicyFor(home), Home: home,
		KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmStaleChilds, StaleAfterHours: 24}}}
	items := Build(cfg, env, nil, time.Now())
	if items[0].Refused == nil || !strings.HasPrefix(items[0].Refused.Error(), "references:") {
		t.Fatalf("expected references refusal, got %v", items[0].Refused)
	}
}

func TestQuarantineTakeRestorePurge(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "sub", "f1"), 100)
	testutil.Write(t, filepath.Join(dir, "f2"), 50)
	p := quarantinePolicy(home)
	now := time.Now()
	q, err := OpenQuarantine(p.QuarantineDir, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	x := &Executor{Policy: p, Log: &log, Now: time.Now, Q: q}
	items := []Item{{Entry: config.Entry{Path: dir, Action: config.ActionRmContents}, Bytes: 150, Deletable: 150}}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("source dir should be empty, has %d", len(ents))
	}
	moved := filepath.Join(p.QuarantineDir, q.Batch, strings.TrimPrefix(dir, "/"), "sub", "f1")
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("file not in quarantine at %s", moved)
	}
	bs, err := ListBatches(p.QuarantineDir)
	if err != nil || len(bs) != 1 || bs[0].Count != 2 {
		t.Fatalf("batches: %+v %v", bs, err)
	}
	if !strings.Contains(log.String(), "quarantine "+filepath.Join(dir, "f2")) {
		t.Error("audit log should record the quarantine move")
	}

	// restore puts everything back and removes the batch
	n, skipped, err := RestoreBatch(p.QuarantineDir, q.Batch, nil)
	if err != nil || n != 2 || len(skipped) != 0 {
		t.Fatalf("restore: n=%d skipped=%v err=%v", n, skipped, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "f1")); err != nil {
		t.Error("f1 not restored")
	}
	if bs, _ := ListBatches(p.QuarantineDir); len(bs) != 0 {
		t.Error("batch dir should be gone after a full restore")
	}

	// restore refuses to clobber: quarantine again, recreate f2, restore
	q2, _ := OpenQuarantine(p.QuarantineDir, now.Add(time.Second), nil)
	x.Q = q2
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, filepath.Join(dir, "f2"), 1)
	n, skipped, err = RestoreBatch(p.QuarantineDir, q2.Batch, nil)
	if err != nil || n != 1 || len(skipped) != 1 {
		t.Fatalf("partial restore: n=%d skipped=%v err=%v", n, skipped, err)
	}
	if bs, _ := ListBatches(p.QuarantineDir); len(bs) != 1 || bs[0].Count != 1 {
		t.Fatalf("batch should remain with 1 entry: %+v", bs)
	}

	// purge: an old batch goes, a fresh one stays; purge-all takes both
	oldName := now.Add(-10 * 24 * time.Hour).Format(BatchLayout)
	oldDir := filepath.Join(p.QuarantineDir, oldName)
	testutil.Write(t, filepath.Join(oldDir, "x", "f"), 10)
	testutil.Write(t, filepath.Join(oldDir, "manifest.json"), 0)
	_ = os.WriteFile(filepath.Join(oldDir, "manifest.json"), []byte(`{"batch":"`+oldName+`","created":"`+now.Add(-10*24*time.Hour).Format(time.RFC3339)+`","entries":[{"from":"/x/f","to":"`+filepath.Join(oldDir, "x", "f")+`","bytes":10}]}`), 0o644)
	freed, names, err := PurgeBatches(p.QuarantineDir, 7*24*time.Hour, now, false)
	if err != nil || len(names) != 1 || names[0] != oldName || freed != 10 {
		t.Fatalf("purge expired: freed=%d names=%v err=%v", freed, names, err)
	}
	if _, err := os.Stat(oldDir); err == nil {
		t.Error("old batch should be gone")
	}
	if bs, _ := ListBatches(p.QuarantineDir); len(bs) != 1 {
		t.Error("fresh batch should survive an expiry purge")
	}
	_, names, err = PurgeBatches(p.QuarantineDir, 7*24*time.Hour, now, true)
	if err != nil || len(names) != 1 {
		t.Fatalf("purge all: %v %v", names, err)
	}
	if bs, _ := ListBatches(p.QuarantineDir); len(bs) != 0 {
		t.Error("purge all should empty the quarantine")
	}
}

func TestQuarantineMoveFailureLeavesSourceIntact(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 10)
	p := quarantinePolicy(home)
	q, _ := OpenQuarantine(p.QuarantineDir, time.Now(), func(src, dst string) error { return errors.New("EXDEV") })
	x := &Executor{Policy: p, Now: time.Now, Q: q}
	items := []Item{{Entry: config.Entry{Path: dir, Action: config.ActionRmContents}, Bytes: 10, Deletable: 10}}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err) // per-item failure is reported, not fatal to the run
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Error("a failed move must leave the source in place")
	}
	if bs, _ := ListBatches(p.QuarantineDir); len(bs) != 1 || bs[0].Count != 0 {
		t.Errorf("manifest must not record a failed move: %+v", bs)
	}
}
