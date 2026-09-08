package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditClassifiesHomeEntries(t *testing.T) {
	home := t.TempDir()
	known := filepath.Join(home, "known-cache")
	write(t, filepath.Join(known, "f"), 65536)
	repo := filepath.Join(home, "project")
	write(t, filepath.Join(repo, ".git", "HEAD"), 10)
	write(t, filepath.Join(repo, "main.go"), 65536)
	stale := filepath.Join(home, ".oldtool")
	write(t, filepath.Join(stale, "data"), 65536)
	age(t, filepath.Join(stale, "data"), 400*24*time.Hour)
	age(t, stale, 400*24*time.Hour)
	cachey := filepath.Join(home, "node_modules")
	write(t, filepath.Join(cachey, "x"), 65536)
	protected := filepath.Join(home, "Documents")
	write(t, filepath.Join(protected, "d"), 65536)
	tiny := filepath.Join(home, "tiny")
	write(t, filepath.Join(tiny, "t"), 1)
	if err := os.Symlink(repo, filepath.Join(home, "lnk")); err != nil {
		t.Fatal(err)
	}

	p := policyFor(home)
	p.NeverTouch = []string{protected}
	cfg := &Config{Version: 1, Volume: home, Policy: p, home: home,
		KnownDirs: []Entry{{Path: known, Type: "cache", Action: ActionRmContents}}}
	rows, err := auditHome(cfg, home, 32768, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]AuditRow{}
	for _, r := range rows {
		got[filepath.Base(r.Path)] = r
	}
	if _, ok := got["tiny"]; ok {
		t.Error("entries under the size floor must be omitted")
	}
	if _, ok := got["log"]; ok {
		// policyFor puts log/state under home; they are tiny or absent, fine either way
	}
	cases := map[string]struct{ status, hint string }{
		"known-cache":  {"known", "rm-contents"},
		"project":      {"unknown", "git repository"},
		".oldtool":     {"unknown", "nothing modified in"},
		"node_modules": {"unknown", "looks like a cache"},
		"Documents":    {"protected", "never_touch"},
	}
	for name, want := range cases {
		r, ok := got[name]
		if !ok {
			t.Errorf("%s missing from audit", name)
			continue
		}
		if r.Status != want.status || !strings.Contains(r.Suggestion, want.hint) {
			t.Errorf("%s: status=%q suggestion=%q, want %q containing %q", name, r.Status, r.Suggestion, want.status, want.hint)
		}
	}
	if !got[".oldtool"].Hidden {
		t.Error("dot entries must be flagged hidden")
	}
	// largest first
	for i := 1; i < len(rows); i++ {
		if rows[i].Bytes > rows[i-1].Bytes {
			t.Fatal("audit rows must be sorted by size descending")
		}
	}
}

func TestDoAuditWritesStateAndSummary(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "big", "f"), 2<<20)
	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home), home: home}
	var out, errw bytes.Buffer
	code := doAudit(cfg, Env{Home: home}, &opts{audit: "~", minMB: 1}, time.Now(), &out, &errw)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "audit of "+home) || !strings.Contains(out.String(), "unknown entries hold") {
		t.Errorf("summary missing: %s", out.String())
	}
	st, _ := loadState(cfg.Policy.StateFile)
	if st.AuditRoot != home || len(st.Audit) == 0 {
		t.Error("audit must be recorded in state")
	}
}
