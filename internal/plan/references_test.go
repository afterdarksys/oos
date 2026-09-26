package plan

import (
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

func TestRmContentsKeepsReferencedChildren(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "idle", "f"), 10)
	testutil.Write(t, filepath.Join(dir, "open", "f"), 10)
	testutil.Write(t, filepath.Join(dir, "cwd", "f"), 10)
	refs := func() ([]string, error) {
		return []string{"/usr/bin/tail -f " + filepath.Join(dir, "open", "f"), filepath.Join(dir, "cwd")}, nil
	}
	x := &Executor{Policy: testutil.PolicyFor(home), Now: time.Now, Refs: refs}
	if _, err := x.Execute([]Item{{Entry: config.Entry{Path: dir, Action: config.ActionRmContents}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "idle")); !os.IsNotExist(err) {
		t.Error("unreferenced child should be removed")
	}
	for _, kept := range []string{"open", "cwd"} {
		if _, err := os.Stat(filepath.Join(dir, kept, "f")); err != nil {
			t.Errorf("%s: a child a running process uses must be kept: %v", kept, err)
		}
	}
}

func TestRmContentsRefusesWithoutReferences(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 10)
	item := []Item{{Entry: config.Entry{Path: dir, Action: config.ActionRmContents}}}
	for name, refs := range map[string]func() ([]string, error){
		"lister fails": func() ([]string, error) { return nil, errors.New("ps failed") },
		"no lister":    nil,
	} {
		x := &Executor{Policy: testutil.PolicyFor(home), Now: time.Now, Refs: refs}
		_, err := x.Execute(item)
		if err == nil || !strings.Contains(err.Error(), "references") {
			t.Errorf("%s: want references refusal, got %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
			t.Fatalf("%s: nothing may be removed: %v", name, err)
		}
	}
}

func TestStaleRecheckUsesNewestChangeInSubtree(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "cache")
	child := filepath.Join(dir, "env")
	testutil.Write(t, filepath.Join(child, "lib", "f"), 4096)
	testutil.AgeTree(t, child, 48*time.Hour)
	cfg := &config.Config{Home: home, Policy: testutil.PolicyFor(home), KnownDirs: []config.Entry{{Path: dir, Action: config.ActionRmStaleChilds, StaleAfterHours: 24}}}
	items := Build(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, nil, time.Now())
	if items[0].Refused != nil || items[0].Deletable == 0 {
		t.Fatalf("fixture should be stale at plan time: %+v", items[0])
	}
	// Between plan and execute something writes deep inside; the top-level
	// mtime is put back so only a subtree walk can see it.
	testutil.Write(t, filepath.Join(child, "lib", "new"), 1)
	testutil.Age(t, filepath.Join(child, "lib"), 48*time.Hour)
	testutil.Age(t, child, 48*time.Hour)
	x := Executor{Home: home, Policy: cfg.Policy, Now: time.Now, Refs: testutil.NoProcs}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(child, "lib", "new")); err != nil {
		t.Fatal("a child written to since planning must be kept")
	}
}
