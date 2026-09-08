//go:build darwin || linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestStaleKeepsChildrenOfRealProcesses spawns real processes and uses the
// real process, cwd and open-file listers: one child is named on a command
// line, one is a process's working directory, one holds an open file, and
// one has nothing. Only the last may be deleted.
func TestStaleKeepsChildrenOfRealProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes and runs lsof")
	}
	home := t.TempDir()
	dir := filepath.Join(home, "a", "archive")
	for _, n := range []string{"bycmd", "bycwd", "byfd", "orphan"} {
		write(t, filepath.Join(dir, n, "f"), 16)
		age(t, filepath.Join(dir, n), 48*time.Hour)
	}
	start := func(name string, args ...string) *exec.Cmd {
		c := exec.Command(name, args...)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Process.Kill(); _, _ = c.Process.Wait() })
		return c
	}
	start("sh", "-c", "sleep 30; true", "sh", filepath.Join(dir, "bycmd")) // path as an ignored positional argument; two commands so sh cannot exec-replace itself and lose the argv
	cwd := exec.Command("sleep", "30")
	cwd.Dir = filepath.Join(dir, "bycwd")
	if err := cwd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cwd.Process.Kill(); _, _ = cwd.Process.Wait() })
	start("tail", "-f", filepath.Join(dir, "byfd", "f"))
	time.Sleep(300 * time.Millisecond)

	env, err := realEnv()
	if err != nil {
		t.Fatal(err)
	}
	env.Home = home
	refs, err := env.references()
	if err != nil {
		t.Fatal(err)
	}
	kids, err := classifyChildren(dir, 24*time.Hour, refs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, k := range kids {
		got[filepath.Base(k.Path)] = k.Keep
	}
	for _, keep := range []string{"bycmd", "bycwd", "byfd"} {
		if got[keep] == "" {
			t.Errorf("%s should be kept (referenced by a live process)", keep)
		}
	}
	if got["orphan"] != "" {
		t.Errorf("orphan should be a delete candidate, got keep=%q", got["orphan"])
	}
	// the delete actually happens only for the orphan
	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home), home: home,
		KnownDirs: []Entry{{Path: dir, Type: "cache", Action: ActionRmStaleChilds, StaleAfterHours: 24}}}
	items := buildPlan(cfg, env, nil, time.Now())
	x := &Executor{Policy: cfg.Policy, Now: time.Now, Refs: env.references}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"bycmd", "bycwd", "byfd"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s was deleted under a live process", keep)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "orphan")); err == nil {
		t.Error("orphan should be gone")
	}
}
