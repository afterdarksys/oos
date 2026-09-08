package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/agent"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestDoWhoReportsProcessesAndNewest(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "proj", "target")
	testutil.Write(t, filepath.Join(home, "proj", "Cargo.toml"), 1)
	testutil.Write(t, filepath.Join(dir, "old"), 1)
	testutil.Age(t, filepath.Join(dir, "old"), time.Hour)
	testutil.Write(t, filepath.Join(dir, "new"), 1)
	cfg := &config.Config{Policy: testutil.PolicyFor(home)}
	env := guard.Env{Home: home,
		Procs: func() ([]string, error) { return []string{"cargo build " + dir + "/debug", "unrelated"}, nil },
		Cwds:  func() ([]string, error) { return []string{dir}, nil }}
	var out, errw bytes.Buffer
	if code := doWho(cfg, env, &opts{who: dir, jsonOut: true}, time.Now(), &out, &errw); code != status.ExitOK {
		t.Fatalf("who: %s", errw.String())
	}
	var r whoResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.UseCase != "Rust build output" || r.Source != "auto" {
		t.Errorf("use case: %s %s", r.UseCase, r.Source)
	}
	if r.ProcessN != 2 || len(r.Processes) != 2 {
		t.Errorf("processes: %d %v", r.ProcessN, r.Processes)
	}
	if filepath.Base(r.NewestFile) != "new" {
		t.Errorf("newest = %s", r.NewestFile)
	}
	out.Reset()
	if code := doWho(cfg, env, &opts{who: filepath.Join(home, "missing")}, time.Now(), &out, &errw); code != status.ExitUsage {
		t.Error("missing path should be a usage error")
	}
}

func TestAgentTickAlertsAndPurges(t *testing.T) {
	home := t.TempDir()
	p := quarantinePolicy(home)
	p.AgentPurgeExpired = true
	p.AlertDropGB = 1
	p.MinFreeGB, p.WarnFreeGB = 0.000001, 0.000002 // real disk reads OK
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	now := time.Now()

	// an expired batch to purge
	oldName := now.Add(-10 * 24 * time.Hour).Format(plan.BatchLayout)
	oldDir := filepath.Join(p.QuarantineDir, oldName)
	testutil.Write(t, filepath.Join(oldDir, "x", "f"), 10)
	_ = os.WriteFile(filepath.Join(oldDir, "manifest.json"), []byte(`{"batch":"`+oldName+`","created":"`+now.Add(-10*24*time.Hour).Format(time.RFC3339)+`","entries":[{"from":"/x/f","to":"`+filepath.Join(oldDir, "x", "f")+`","bytes":10}]}`), 0o644)

	var notes []string
	old := agent.Notify
	agent.Notify = func(title, msg string) error { notes = append(notes, msg); return nil }
	defer func() { agent.Notify = old }()

	// first tick: nothing to compare against, status OK, purge runs
	var out, errw bytes.Buffer
	if code := agent.Tick(cfg, false, now, &out, &errw); code != status.ExitOK {
		t.Fatalf("tick 1 code=%d %s", code, errw.String())
	}
	if len(notes) != 0 {
		t.Errorf("first OK tick should not notify: %v", notes)
	}
	if _, err := os.Stat(oldDir); err == nil {
		t.Error("expired batch should have been purged by the agent")
	}
	// fake a previous reading far above what the disk has now
	st, _ := state.Load(p.StateFile)
	st.History[len(st.History)-1].FreeGB = 1e6
	st.History[len(st.History)-1].At = now.Add(-time.Hour)
	_ = state.Save(p.StateFile, st)
	out.Reset()
	if code := agent.Tick(cfg, true, now, &out, &errw); code != status.ExitOK {
		t.Fatalf("tick 2 code=%d", code)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "dropped") {
		t.Errorf("drop alert expected once, got %v", notes)
	}
	// a stale previous reading (4h ago) must not alert
	st, _ = state.Load(p.StateFile)
	st.History = st.History[:0]
	st.History = append(st.History, state.HistoryPoint{At: now.Add(-4 * time.Hour), FreeGB: 1e6, Event: "agent"})
	_ = state.Save(p.StateFile, st)
	notes = nil
	agent.Tick(cfg, false, now, &out, &errw)
	if len(notes) != 0 {
		t.Errorf("stale reading must not alert: %v", notes)
	}
	// critical status alerts regardless
	cfg.Policy.MinFreeGB, cfg.Policy.WarnFreeGB = 1e9, 2e9
	notes = nil
	if code := agent.Tick(cfg, false, now, &out, &errw); code != status.ExitCritical || len(notes) != 1 {
		t.Errorf("critical tick: code=%d notes=%v", code, notes)
	}
}
