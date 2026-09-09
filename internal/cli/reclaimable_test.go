package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/testutil"
)

// TestCleanupCommandOnlyLeavesNoBatch: a live run whose only work was a
// command must not leave an empty quarantine batch behind. On 2026-09-08
// `oos -C -t vm -y` created batch 20260908-210611 holding nothing.
func TestCleanupCommandOnlyLeavesNoBatch(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "vm")
	testutil.Write(t, filepath.Join(dir, "disk.raw"), 10)
	p := quarantinePolicy(home)
	p.AllowCommands = true
	p.MinFreeGB, p.WarnFreeGB = 0.000001, 0.000002
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home,
		KnownDirs: []config.Entry{{Path: dir, Type: "vm", Action: config.ActionCommand, Command: "true"}}}
	env := guard.Env{Home: home, Procs: testutil.NoProcs, Cwds: testutil.NoProcs}
	var out, errw bytes.Buffer
	code := doCleanup(cfg, env, &opts{cleanup: true, yes: true}, time.Now(), &out, &errw)
	if code != status.ExitOK {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errw.String())
	}
	if bs, _ := plan.ListBatches(p.QuarantineDir); len(bs) != 0 {
		t.Errorf("command-only run left a batch: %+v", bs)
	}
	if ents, _ := os.ReadDir(p.QuarantineDir); len(ents) != 0 {
		t.Errorf("quarantine dir should hold no batch directory: %v", ents)
	}
	if !strings.Contains(out.String(), "nothing quarantined") {
		t.Errorf("output should say nothing was quarantined:\n%s", out.String())
	}
}

// TestPurgeReportsMeasuredDelta: the manifest total is what was recorded
// when the batch was taken; what a purge gives back is what the volume
// says. Both appear, and the recorded figure is labelled as such.
func TestPurgeReportsMeasuredDelta(t *testing.T) {
	home := t.TempDir()
	p := quarantinePolicy(home)
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	now := time.Now()
	name := now.Add(-time.Hour).Format(plan.BatchLayout)
	bdir := filepath.Join(p.QuarantineDir, name)
	testutil.Write(t, filepath.Join(bdir, "x", "f"), 1<<20)
	man := `{"batch":"` + name + `","created":"` + now.Add(-time.Hour).Format(time.RFC3339) + `","entries":[{"from":"/x/f","to":"` + filepath.Join(bdir, "x", "f") + `","bytes":1048576}]}`
	if err := os.WriteFile(filepath.Join(bdir, "manifest.json"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := doPurge(cfg, &opts{purge: true, purgeNow: true}, now, &out, &errw); code != status.ExitOK {
		t.Fatalf("dry run code=%d %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "recorded") || !strings.Contains(out.String(), "dry-run") {
		t.Errorf("dry run should label the recorded total:\n%s", out.String())
	}
	out.Reset()
	if code := doPurge(cfg, &opts{purge: true, purgeNow: true, yes: true}, now, &out, &errw); code != status.ExitOK {
		t.Fatalf("purge code=%d %s", code, errw.String())
	}
	s := out.String()
	if !strings.Contains(s, "purged 1 batches") || !strings.Contains(s, "recorded 1.0 MB") || !strings.Contains(s, "volume free") {
		t.Errorf("purge should report recorded bytes and the measured volume change:\n%s", s)
	}
	if _, err := os.Stat(bdir); !os.IsNotExist(err) {
		t.Error("batch should be gone")
	}
}
