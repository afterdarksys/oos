package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryTrend(t *testing.T) {
	home := t.TempDir()
	p := policyFor(home)
	st := &State{Known: map[string]int64{}}
	now := time.Now()
	st.History = []HistoryPoint{
		{At: now.Add(-48 * time.Hour), FreeGB: 100, Event: "agent"},
		{At: now, FreeGB: 80, Event: "check"}, // out of order on purpose: a long run records its start time when it ends
		{At: now.Add(-24 * time.Hour), FreeGB: 90, Event: "agent"},
	}
	if err := saveState(p.StateFile, st); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Version: 1, Volume: home, Policy: p, home: home}
	var out bytes.Buffer
	doHistory(cfg, &opts{history: 2}, &out)
	s := out.String()
	if strings.Count(s, "\n") != 3 || !strings.Contains(s, "-10.0 GB over 24h0m0s") || !strings.Contains(s, "trend: -10.0 GB/day") {
		t.Errorf("history output:\n%s", s)
	}
	out.Reset()
	doHistory(&Config{Policy: Policy{StateFile: filepath.Join(home, "none.json")}}, &opts{history: 5}, &out)
	if !strings.Contains(out.String(), "no readings") {
		t.Error("missing state should say so")
	}

	// first deploy on apps: a check and a tick seconds apart printed
	// "-239.2 GB/day over 0s". A window under an hour gets no rate.
	st.History = []HistoryPoint{
		{At: now.Add(-20 * time.Second), FreeGB: 268.7, Event: "check"},
		{At: now, FreeGB: 268.6, Event: "agent"},
	}
	if err := saveState(p.StateFile, st); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	doHistory(cfg, &opts{history: 2}, &out)
	if strings.Contains(out.String(), "GB/day") || !strings.Contains(out.String(), "trend: needs 1h0m0s of readings") {
		t.Errorf("sub-hour span must not extrapolate:\n%s", out.String())
	}
}

func TestCleanupPermanentBypassesQuarantine(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(dir, "f"), 10)
	cfg := &Config{Version: 1, Volume: home, Policy: quarantinePolicy(home), home: home,
		KnownDirs: []Entry{{Path: dir, Type: "cache", Action: ActionRmContents}}}
	var out, errw bytes.Buffer
	doCleanup(cfg, Env{Home: home, Procs: noProcs}, &opts{cleanup: true, yes: true, permanent: true}, time.Now(), &out, &errw)
	if _, err := os.Stat(filepath.Join(dir, "f")); err == nil {
		t.Fatal("file should be gone")
	}
	if bs, _ := listBatches(cfg.Policy.QuarantineDir); len(bs) != 0 {
		t.Error("permanent run must not create a quarantine batch")
	}
	if !strings.Contains(out.String(), "permanent delete") || strings.Contains(out.String(), "quarantine batch") {
		t.Errorf("output should say permanent: %s", out.String())
	}
}
