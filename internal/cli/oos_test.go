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
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestCleanupDryRunByDefault(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 10)
	cfg := &config.Config{Version: 1, Volume: home, Policy: testutil.PolicyFor(home),
		KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmContents}}}
	env := guard.Env{Home: home, Procs: testutil.NoProcs}
	var out, errw bytes.Buffer

	// no --yes
	doCleanup(cfg, env, &opts{cleanup: true}, time.Now(), &out, &errw)
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Fatal("dry-run deleted a file")
	}
	if !strings.Contains(out.String(), "dry-run") {
		t.Error("expected dry-run banner")
	}
	// --yes --no: --no wins
	out.Reset()
	doCleanup(cfg, env, &opts{cleanup: true, yes: true, no: true}, time.Now(), &out, &errw)
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Fatal("--no must force dry-run even with --yes")
	}
	// --yes: live
	out.Reset()
	doCleanup(cfg, env, &opts{cleanup: true, yes: true}, time.Now(), &out, &errw)
	if _, err := os.Stat(filepath.Join(dir, "f")); err == nil {
		t.Fatal("--yes should have deleted the file")
	}
	if _, err := os.Stat(cfg.Policy.LogFile); err != nil {
		t.Error("audit log must exist after a live run")
	}
}

// --- sizing and scan ---

func TestPathSizeIgnoresSymlinkTargets(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "d")
	testutil.Write(t, filepath.Join(dir, "f"), 8192)
	big := filepath.Join(home, "big")
	testutil.Write(t, filepath.Join(big, "b"), 1<<20)
	if err := os.Symlink(big, filepath.Join(dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	n, err := size.PathSize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n >= 1<<20 {
		t.Errorf("size %d counted the symlink target", n)
	}
	if n < 8192 {
		t.Errorf("size %d too small", n)
	}
}

func TestFlagsShortAndLong(t *testing.T) {
	var errw bytes.Buffer
	o, err := parseFlags([]string{"-C", "--yes", "-t", "cache,vm", "--config", "x.json", "-q"}, &errw)
	if err != nil {
		t.Fatal(err)
	}
	if !o.cleanup || !o.yes || o.types != "cache,vm" || o.config != "x.json" || !o.quick {
		t.Errorf("flags not parsed: %+v", o)
	}
	o, err = parseFlags(nil, &errw)
	if err != nil || !o.check || !o.quick {
		t.Errorf("no args should default to quick check: %+v %v", o, err)
	}
	if _, err := parseFlags([]string{"stray"}, &errw); err == nil {
		t.Error("positional args must be rejected")
	}
}
