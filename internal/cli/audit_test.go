package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestDoAuditWritesStateAndSummary(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, filepath.Join(home, "big", "f"), 2<<20)
	cfg := &config.Config{Version: 1, Volume: home, Policy: testutil.PolicyFor(home), Home: home}
	var out, errw bytes.Buffer
	code := doAudit(cfg, guard.Env{Home: home}, &opts{audit: "~", minMB: 1}, time.Now(), &out, &errw)
	if code != status.ExitOK {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "audit of "+home) || !strings.Contains(out.String(), "unknown entries hold") {
		t.Errorf("summary missing: %s", out.String())
	}
	st, _ := state.Load(cfg.Policy.StateFile)
	if st.AuditRoot != home || len(st.Audit) == 0 {
		t.Error("audit must be recorded in state")
	}
}
