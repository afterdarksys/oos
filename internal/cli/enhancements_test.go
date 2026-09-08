package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/agent"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/testutil"
)

func quarantinePolicy(home string) config.Policy {
	p := testutil.PolicyFor(home)
	p.Quarantine = true
	p.QuarantineDir = filepath.Join(home, "q")
	p.QuarantineDays = 7
	return p
}

func TestDoCleanupQuarantinesAndReportsBatch(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 10)
	cfg := &config.Config{Version: 1, Volume: home, Policy: quarantinePolicy(home), Home: home,
		KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmContents}}}
	env := guard.Env{Home: home, Procs: testutil.NoProcs}
	var out, errw bytes.Buffer
	doCleanup(cfg, env, &opts{cleanup: true, yes: true}, time.Now(), &out, &errw)
	if _, err := os.Stat(filepath.Join(dir, "f")); err == nil {
		t.Fatal("file should have been moved")
	}
	if !strings.Contains(out.String(), "moved to quarantine batch") || !strings.Contains(out.String(), "--restore") {
		t.Errorf("output should name the batch and the undo: %s", out.String())
	}
	if bs, _ := plan.ListBatches(cfg.Policy.QuarantineDir); len(bs) != 1 {
		t.Error("expected one batch")
	}
}

func TestDiffReportsGrowth(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 1<<20)
	p := testutil.PolicyFor(home)
	st := &state.State{Known: map[string]int64{dir: 1024}}
	st.Record("check", size.DiskUsage{Free: 10 << 30, Total: 20 << 30}, time.Now().Add(-time.Hour))
	if err := state.Save(p.StateFile, st); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home,
		KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmContents}}}
	var out, errw bytes.Buffer
	doDiff(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, &opts{diff: true}, time.Now(), &out, &errw)
	if !strings.Contains(out.String(), "+") || !strings.Contains(out.String(), dir) {
		t.Errorf("diff should show growth for %s: %s", dir, out.String())
	}
	got, _ := state.Load(p.StateFile)
	if got.Known[dir] <= 1024 {
		t.Error("diff must record the new size")
	}
}

func TestNotifyOnlyWhenNotOK(t *testing.T) {
	home := t.TempDir()
	calls := 0
	old := agent.Notify
	agent.Notify = func(title, msg string) error { calls++; return nil }
	defer func() { agent.Notify = old }()
	p := testutil.PolicyFor(home)
	p.MinFreeGB, p.WarnFreeGB = 0.000001, 0.000002 // any real disk is OK
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	var out, errw bytes.Buffer
	doCheck(cfg, guard.Env{Home: home}, &opts{check: true, quick: true, notify: true}, time.Now(), &out, &errw)
	if calls != 0 {
		t.Error("OK status must not notify")
	}
	p.MinFreeGB, p.WarnFreeGB = 1e9, 2e9 // any real disk is CRITICAL
	cfg.Policy = p
	code := doCheck(cfg, guard.Env{Home: home}, &opts{check: true, quick: true, notify: true}, time.Now(), &out, &errw)
	if calls != 1 || code != status.ExitCritical {
		t.Errorf("critical should notify once and exit 2: calls=%d code=%d", calls, code)
	}
}

func TestVersionFlag(t *testing.T) {
	var out, errw bytes.Buffer
	if code := Run([]string{"-V"}, &out, &errw); code != status.ExitOK || !strings.Contains(out.String(), Version) {
		t.Errorf("version flag: code=%d out=%q", code, out.String())
	}
}
