package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/testutil"
)

// TestPurgeNowSkipsHeldBatches: --purge-now --yes must not delete a batch
// holding unrecorded data (or a pending/changed entry) unless --include-held
// is also given, and it says which batch it kept and why.
func TestPurgeNowSkipsHeldBatches(t *testing.T) {
	home := t.TempDir()
	p := quarantinePolicy(home)
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	now := time.Now()
	name := now.Add(-time.Hour).Format(plan.BatchLayout)
	bdir := filepath.Join(p.QuarantineDir, name)
	testutil.Write(t, filepath.Join(bdir, "stray"), 4096)
	man := `{"batch":"` + name + `","created":"` + now.Add(-time.Hour).Format(time.RFC3339) + `","entries":[]}`
	if err := os.WriteFile(filepath.Join(bdir, "manifest.json"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := doPurge(cfg, &opts{purgeNow: true, yes: true}, now, &out, &errw); code != status.ExitNothing {
		t.Fatalf("purge code=%d %s", code, errw.String())
	}
	s := out.String()
	if !strings.Contains(s, "held") || !strings.Contains(s, name) || !strings.Contains(s, "unrecorded") || !strings.Contains(s, "purged 0 batches") {
		t.Errorf("held batch should be reported and kept:\n%s", s)
	}
	if _, err := os.Stat(filepath.Join(bdir, "stray")); err != nil {
		t.Fatal("held batch deleted by --purge-now", err)
	}
	out.Reset()
	if code := doPurge(cfg, &opts{purgeNow: true, includeHeld: true, yes: true}, now, &out, &errw); code != status.ExitOK {
		t.Fatalf("purge code=%d %s", code, errw.String())
	}
	if _, err := os.Stat(bdir); !os.IsNotExist(err) {
		t.Fatalf("--include-held should delete the held batch:\n%s", out.String())
	}
}

func TestIncludeHeldNeedsPurge(t *testing.T) {
	var errw bytes.Buffer
	if _, err := parseFlags([]string{"--include-held"}, &errw); err == nil {
		t.Fatal("--include-held accepted without --purge")
	}
	o, err := parseFlags([]string{"--purge-now", "--include-held", "--yes"}, &errw)
	if err != nil || !o.includeHeld || !o.purgeNow {
		t.Fatalf("flags not parsed: %+v %v", o, err)
	}
}
