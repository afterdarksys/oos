package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOwnerForPrefixAndGlob(t *testing.T) {
	home := t.TempDir()
	cfg := &Config{Policy: Policy{Owners: []Owner{
		{Match: filepath.Join(home, "development", "**", "src-tauri", "target"), UseCase: "Tauri build output"},
		{Match: filepath.Join(home, "development", "**", "target"), UseCase: "Rust build output"},
		{Match: filepath.Join(home, "development"), UseCase: "source trees"},
		{Match: filepath.Join(home, ".cache", "uv"), UseCase: "uv"},
	}}}
	cases := map[string]string{
		filepath.Join(home, "development", "foo", "target"):                           "Rust build output",
		filepath.Join(home, "development", "foo", "target", "debug"):                  "Rust build output",
		filepath.Join(home, "development", "foo", "src-tauri", "target"):              "Tauri build output",
		filepath.Join(home, "development", "a", "b", "c", "src-tauri", "target", "x"): "Tauri build output",
		filepath.Join(home, "development", "deep", "er", "target"):                    "Rust build output",
		filepath.Join(home, "development", "foo", "src"):                              "source trees",
		filepath.Join(home, "development"):                                            "source trees",
		filepath.Join(home, ".cache", "uv", "archive-v0", "x"):                        "uv",
		filepath.Join(home, ".cache", "uvx"):                                          "",
		filepath.Join(home, "elsewhere"):                                              "",
	}
	for p, want := range cases {
		o, ok := cfg.ownerFor(p)
		got := ""
		if ok {
			got = o.UseCase
		}
		if got != want {
			t.Errorf("ownerFor(%s) = %q, want %q", p, got, want)
		}
	}
}

func TestAttributeFingerprints(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "proj")
	write(t, filepath.Join(repo, ".git", "HEAD"), 1)
	write(t, filepath.Join(repo, "Cargo.toml"), 1)
	write(t, filepath.Join(repo, "target", "debug", "bin"), 1)
	write(t, filepath.Join(repo, "web", "package.json"), 1)
	write(t, filepath.Join(repo, "web", "node_modules", "x", "index.js"), 1)
	write(t, filepath.Join(home, "venv", "pyvenv.cfg"), 1)
	write(t, filepath.Join(home, "plain", "f"), 1)

	cases := map[string]struct{ label, fp, repo string }{
		filepath.Join(repo, "target"):              {"Rust build output (proj)", "Cargo.toml beside target", "proj"},
		filepath.Join(repo, "web", "node_modules"): {"npm dependencies (proj)", "package.json beside node_modules", "proj"},
		filepath.Join(home, "venv"):                {"Python virtualenv", "pyvenv.cfg", ""},
		repo:                                       {"git repository", ".git", ""},
		filepath.Join(repo, "web"):                 {"Node project (proj)", "package.json", "proj"},
		filepath.Join(home, "plain"):               {"", "", ""},
	}
	for p, want := range cases {
		a := attribute(p)
		if a.Label != want.label || a.Fingerprint != want.fp || a.Repo != want.repo {
			t.Errorf("attribute(%s) = %+v, want %+v", p, a, want)
		}
	}
}

func TestUseCasePrecedence(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "proj", "target")
	write(t, filepath.Join(home, "proj", "Cargo.toml"), 1)
	write(t, filepath.Join(dir, "x"), 1)
	cfg := &Config{Policy: Policy{Owners: []Owner{{Match: filepath.Join(home, "proj"), UseCase: "from owner"}}},
		KnownDirs: []Entry{{Path: dir, Type: "build", Action: ActionNever, UseCase: "from entry"}}}
	if l, s := cfg.useCaseFor(dir); l != "from entry" || s != "entry" {
		t.Errorf("entry should win: %s %s", l, s)
	}
	if l, s := cfg.useCaseFor(filepath.Join(home, "proj", "src")); l != "from owner" || s != "owner" {
		t.Errorf("owner should be second: %s %s", l, s)
	}
	cfg.Policy.Owners = nil
	cfg.KnownDirs = nil
	if l, s := cfg.useCaseFor(dir); l != "Rust build output" || s != "auto" {
		t.Errorf("auto should be last: %s %s", l, s)
	}
	if l, _ := cfg.useCaseFor(filepath.Join(home, "nothing")); l != "" {
		t.Errorf("unknown should be empty, got %q", l)
	}
}

func TestGroupByUseCase(t *testing.T) {
	cfg := &Config{Policy: Policy{Owners: []Owner{{Match: "/a", UseCase: "A"}}}}
	totals := groupByUseCase(cfg, []string{"/a/x", "/a/y", "/b"}, []int64{5, 7, 3})
	if len(totals) != 2 || totals[0].UseCase != "A" || totals[0].Bytes != 12 || totals[0].Count != 2 || totals[1].UseCase != "unattributed" {
		t.Errorf("totals = %+v", totals)
	}
}

func TestDoWhoReportsProcessesAndNewest(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "proj", "target")
	write(t, filepath.Join(home, "proj", "Cargo.toml"), 1)
	write(t, filepath.Join(dir, "old"), 1)
	age(t, filepath.Join(dir, "old"), time.Hour)
	write(t, filepath.Join(dir, "new"), 1)
	cfg := &Config{Policy: policyFor(home)}
	env := Env{Home: home,
		Procs: func() ([]string, error) { return []string{"cargo build " + dir + "/debug", "unrelated"}, nil },
		Cwds:  func() ([]string, error) { return []string{dir}, nil }}
	var out, errw bytes.Buffer
	if code := doWho(cfg, env, &opts{who: dir, jsonOut: true}, time.Now(), &out, &errw); code != exitOK {
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
	if code := doWho(cfg, env, &opts{who: filepath.Join(home, "missing")}, time.Now(), &out, &errw); code != exitUsage {
		t.Error("missing path should be a usage error")
	}
}

func TestAgentTickAlertsAndPurges(t *testing.T) {
	home := t.TempDir()
	p := quarantinePolicy(home)
	p.AgentPurgeExpired = true
	p.AlertDropGB = 1
	p.MinFreeGB, p.WarnFreeGB = 0.000001, 0.000002 // real disk reads OK
	cfg := &Config{Version: 1, Volume: home, Policy: p, home: home}
	now := time.Now()

	// an expired batch to purge
	oldName := now.Add(-10 * 24 * time.Hour).Format(batchLayout)
	oldDir := filepath.Join(p.QuarantineDir, oldName)
	write(t, filepath.Join(oldDir, "x", "f"), 10)
	_ = os.WriteFile(filepath.Join(oldDir, "manifest.json"), []byte(`{"batch":"`+oldName+`","created":"`+now.Add(-10*24*time.Hour).Format(time.RFC3339)+`","entries":[{"from":"/x/f","to":"`+filepath.Join(oldDir, "x", "f")+`","bytes":10}]}`), 0o644)

	var notes []string
	old := notifyFn
	notifyFn = func(title, msg string) error { notes = append(notes, msg); return nil }
	defer func() { notifyFn = old }()

	// first tick: nothing to compare against, status OK, purge runs
	var out, errw bytes.Buffer
	if code := doAgentTick(cfg, &opts{}, now, &out, &errw); code != exitOK {
		t.Fatalf("tick 1 code=%d %s", code, errw.String())
	}
	if len(notes) != 0 {
		t.Errorf("first OK tick should not notify: %v", notes)
	}
	if _, err := os.Stat(oldDir); err == nil {
		t.Error("expired batch should have been purged by the agent")
	}
	// fake a previous reading far above what the disk has now
	st, _ := loadState(p.StateFile)
	st.History[len(st.History)-1].FreeGB = 1e6
	st.History[len(st.History)-1].At = now.Add(-time.Hour)
	_ = saveState(p.StateFile, st)
	out.Reset()
	if code := doAgentTick(cfg, &opts{jsonOut: true}, now, &out, &errw); code != exitOK {
		t.Fatalf("tick 2 code=%d", code)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "dropped") {
		t.Errorf("drop alert expected once, got %v", notes)
	}
	// a stale previous reading (4h ago) must not alert
	st, _ = loadState(p.StateFile)
	st.History = st.History[:0]
	st.History = append(st.History, HistoryPoint{At: now.Add(-4 * time.Hour), FreeGB: 1e6, Event: "agent"})
	_ = saveState(p.StateFile, st)
	notes = nil
	doAgentTick(cfg, &opts{}, now, &out, &errw)
	if len(notes) != 0 {
		t.Errorf("stale reading must not alert: %v", notes)
	}
	// critical status alerts regardless
	cfg.Policy.MinFreeGB, cfg.Policy.WarnFreeGB = 1e9, 2e9
	notes = nil
	if code := doAgentTick(cfg, &opts{}, now, &out, &errw); code != exitCritical || len(notes) != 1 {
		t.Errorf("critical tick: code=%d notes=%v", code, notes)
	}
}

func TestOwnersValidation(t *testing.T) {
	base := `{"version":1,"volume":"/","policy":{"min_free_gb":1,"warn_free_gb":2,"max_delete_gb_per_run":1,"min_path_depth":3,"log_file":"~/l","state_file":"~/s","big_file_min_mb":1,"scan_top_n":1,"owners":[%s]}}`
	if _, err := parseConfig([]byte(strings.Replace(base, "%s", `{"match":"relative","use_case":"x"}`, 1)), "/h"); err == nil {
		t.Error("relative owner match must be rejected")
	}
	if _, err := parseConfig([]byte(strings.Replace(base, "%s", `{"match":"~/a","use_case":""}`, 1)), "/h"); err == nil {
		t.Error("empty use_case must be rejected")
	}
	cfg, err := parseConfig([]byte(strings.Replace(base, "%s", `{"match":"~/a/*/target","use_case":"rust"}`, 1)), "/h")
	if err != nil || cfg.Policy.Owners[0].Match != "/h/a/*/target" {
		t.Errorf("owner match should expand ~: %v %+v", err, cfg)
	}
}

func TestGlobRegexp(t *testing.T) {
	cases := []struct {
		pat, p string
		want   bool
	}{
		{"/d/**/target", "/d/a/target", true},
		{"/d/**/target", "/d/a/b/c/target", true},
		{"/d/**/target", "/d/target", true},
		{"/d/**/target", "/d/a/targets", false},
		{"/d/*/target", "/d/a/target", true},
		{"/d/*/target", "/d/a/b/target", false},
		{"/d/?/x", "/d/a/x", true},
		{"/d/?/x", "/d/ab/x", false},
		{"/d/lit.eral", "/d/lit.eral", true},
		{"/d/lit.eral", "/d/litXeral", false},
	}
	for _, c := range cases {
		re, err := globRegexp(c.pat)
		if err != nil {
			t.Fatalf("%s: %v", c.pat, err)
		}
		if got := re.MatchString(c.p); got != c.want {
			t.Errorf("%s ~ %s = %v, want %v", c.pat, c.p, got, c.want)
		}
	}
}
