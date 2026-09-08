package plan

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestExecuteRmContentsKeepsDirAndDoesNotFollowSymlinks(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	target := filepath.Join(home, "a", "precious")
	testutil.Write(t, filepath.Join(dir, "sub", "f1"), 100)
	testutil.Write(t, filepath.Join(dir, "f2"), 50)
	testutil.Write(t, filepath.Join(target, "t"), 7)
	if err := os.Symlink(target, filepath.Join(dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	// read-only tree, like a Go module cache
	ro := filepath.Join(dir, "ro", "pkg")
	testutil.Write(t, filepath.Join(ro, "g"), 3)
	_ = os.Chmod(filepath.Join(ro, "g"), 0o444)
	_ = os.Chmod(ro, 0o555)

	var log bytes.Buffer
	x := &Executor{Policy: testutil.PolicyFor(home), Log: &log, Now: time.Now}
	items := []Item{{Entry: config.Entry{Path: dir, Action: config.ActionRmContents}, Bytes: 160}}
	freed, err := x.Execute(items)
	if err != nil {
		t.Fatal(err)
	}
	if freed <= 0 {
		t.Error("expected bytes freed")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("dir itself must survive rm-contents")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("dir should be empty, has %d entries", len(ents))
	}
	if _, err := os.Stat(filepath.Join(target, "t")); err != nil {
		t.Error("symlink target must be untouched")
	}
	if !strings.Contains(log.String(), "rm-contents "+dir) {
		t.Error("audit log missing rm-contents line")
	}
}

func TestExecuteRefusesOverBudgetBeforeTouchingAnything(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 10)
	p := testutil.PolicyFor(home)
	p.MaxDeleteGBPerRun = 0.000001 // ~1 KB
	x := &Executor{Policy: p, Now: time.Now}
	items := []Item{{Entry: config.Entry{Path: dir, Action: config.ActionRmContents}, Bytes: 5 * 1024, Deletable: 5 * 1024}}
	if _, err := x.Execute(items); err == nil || !strings.HasPrefix(err.Error(), "budget:") {
		t.Fatalf("expected budget refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Error("nothing may be deleted when the budget refuses the run")
	}
}

func TestExecuteSkipsRefusedAndHonoursAllowCommands(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 10)
	ran := 0
	p := testutil.PolicyFor(home)
	p.AllowCommands = false
	x := &Executor{Policy: p, Now: time.Now, Run: func(string) error { ran++; return nil }}
	items := []Item{
		{Entry: config.Entry{Path: dir, Action: config.ActionRmContents}, Bytes: 10, Refused: guard.Refuse("test", "no")},
		{Entry: config.Entry{Path: "/x/y/z", Action: config.ActionCommand, Command: "true"}},
	}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Error("refused item must not be deleted")
	}
	if ran != 0 {
		t.Error("command must not run when allow_commands is false")
	}
	p.AllowCommands = true
	x.Policy = p
	if _, err := x.Execute(items[1:]); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Errorf("command should run once, ran %d", ran)
	}
}
