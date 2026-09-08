package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func age(t *testing.T, p string, d time.Duration) {
	t.Helper()
	ts := time.Now().Add(-d)
	if err := os.Chtimes(p, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func staleFixture(t *testing.T) (home, dir string) {
	t.Helper()
	home = t.TempDir()
	dir = filepath.Join(home, "a", "archive")
	write(t, filepath.Join(dir, "oldunref", "f"), 4096)
	age(t, filepath.Join(dir, "oldunref"), 48*time.Hour)
	write(t, filepath.Join(dir, "oldref", "bin", "python"), 4096)
	age(t, filepath.Join(dir, "oldref"), 48*time.Hour)
	write(t, filepath.Join(dir, "fresh", "f"), 4096)
	write(t, filepath.Join(dir, "cwdref", "f"), 4096)
	age(t, filepath.Join(dir, "cwdref"), 48*time.Hour)
	write(t, filepath.Join(dir, ".lock"), 1)
	age(t, filepath.Join(dir, ".lock"), 48*time.Hour)
	if err := os.Symlink(filepath.Join(home, "a"), filepath.Join(dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	return home, dir
}

func TestReferencedMatchesWholeComponents(t *testing.T) {
	refs := []string{"/usr/bin/python /x/archive/abc/bin/python serve", "cwd:/x/archive/def"}
	if !referenced("/x/archive/abc", refs) {
		t.Error("cmdline reference missed")
	}
	if !referenced("/x/archive/def", refs) {
		t.Error("cwd reference missed")
	}
	if referenced("/x/archive/ab", refs) {
		t.Error("prefix must not match a longer component")
	}
	if referenced("/x/archive/zzz", refs) {
		t.Error("unreferenced path matched")
	}
}

func TestClassifyChildren(t *testing.T) {
	_, dir := staleFixture(t)
	refs := []string{
		"/usr/local/bin/uv tool uvx " + filepath.Join(dir, "oldref", "bin", "python") + " -m server",
		filepath.Join(dir, "cwdref"),
	}
	kids, err := classifyChildren(dir, 24*time.Hour, refs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, k := range kids {
		got[filepath.Base(k.Path)] = k.Keep
	}
	if got["oldunref"] != "" {
		t.Errorf("oldunref should be a delete candidate, got keep=%q", got["oldunref"])
	}
	for name, want := range map[string]string{"oldref": "referenced", "cwdref": "referenced", "fresh": "floor", ".lock": "lock", "lnk": "symlink"} {
		if !strings.Contains(got[name], want) {
			t.Errorf("%s: keep=%q, want it to mention %q", name, got[name], want)
		}
	}
}

func TestStalePlanAndExecuteDeletesOnlyStaleUnreferenced(t *testing.T) {
	home, dir := staleFixture(t)
	ref := "/usr/local/bin/uv tool uvx " + filepath.Join(dir, "oldref", "bin", "python")
	env := Env{Home: home,
		Procs: func() ([]string, error) { return []string{ref}, nil },
		Cwds:  func() ([]string, error) { return []string{filepath.Join(dir, "cwdref")}, nil }}
	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home), home: home,
		KnownDirs: []Entry{{Path: dir, Type: "cache", Action: ActionRmStaleChilds, StaleAfterHours: 24}}}
	items := buildPlan(cfg, env, nil, time.Now())
	if len(items) != 1 || items[0].Refused != nil {
		t.Fatalf("plan: %+v", items)
	}
	if items[0].Deletable <= 0 || items[0].Deletable >= items[0].Bytes {
		t.Fatalf("deletable %d should be a strict subset of %d", items[0].Deletable, items[0].Bytes)
	}
	var log bytes.Buffer
	x := &Executor{Policy: cfg.Policy, Log: &log, Now: time.Now, Refs: env.references}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "oldunref")); err == nil {
		t.Error("oldunref should be gone")
	}
	for _, keep := range []string{"oldref", "fresh", "cwdref", ".lock", "lnk"} {
		if _, err := os.Lstat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s should survive", keep)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "a", "archive")); err != nil {
		t.Error("the directory itself must survive")
	}
}

func TestStaleRecheckAtExecuteTime(t *testing.T) {
	home, dir := staleFixture(t)
	empty := func() ([]string, error) { return nil, nil }
	env := Env{Home: home, Procs: empty, Cwds: empty}
	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home), home: home,
		KnownDirs: []Entry{{Path: dir, Type: "cache", Action: ActionRmStaleChilds, StaleAfterHours: 24}}}
	items := buildPlan(cfg, env, nil, time.Now())
	// Between plan and execute, oldunref becomes referenced.
	late := func() ([]string, error) { return []string{"python " + filepath.Join(dir, "oldunref") + "/f"}, nil }
	x := &Executor{Policy: cfg.Policy, Now: time.Now, Refs: late}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "oldunref")); err != nil {
		t.Error("a child referenced at execute time must be kept")
	}
	// And with no reference lister at all, the item is refused, not deleted:
	// oldunref is still a delete candidate in the plan and must survive.
	x = &Executor{Policy: cfg.Policy, Now: time.Now}
	_, _ = x.Execute(items)
	if _, err := os.Stat(filepath.Join(dir, "oldunref")); err != nil {
		t.Error("blind execute must not delete")
	}
}

func TestStaleFailsClosedWhenReferencesUnavailable(t *testing.T) {
	home, dir := staleFixture(t)
	env := Env{Home: home, Procs: func() ([]string, error) { return nil, errors.New("ps broke") }}
	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home), home: home,
		KnownDirs: []Entry{{Path: dir, Type: "cache", Action: ActionRmStaleChilds, StaleAfterHours: 24}}}
	items := buildPlan(cfg, env, nil, time.Now())
	if items[0].Refused == nil || !strings.HasPrefix(items[0].Refused.Error(), "references:") {
		t.Fatalf("expected references refusal, got %v", items[0].Refused)
	}
}

func quarantinePolicy(home string) Policy {
	p := policyFor(home)
	p.Quarantine = true
	p.QuarantineDir = filepath.Join(home, "q")
	p.QuarantineDays = 7
	return p
}

func TestQuarantineTakeRestorePurge(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(dir, "sub", "f1"), 100)
	write(t, filepath.Join(dir, "f2"), 50)
	p := quarantinePolicy(home)
	now := time.Now()
	q, err := openQuarantine(p.QuarantineDir, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	x := &Executor{Policy: p, Log: &log, Now: time.Now, Q: q}
	items := []PlanItem{{Entry: Entry{Path: dir, Action: ActionRmContents}, Bytes: 150, Deletable: 150}}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("source dir should be empty, has %d", len(ents))
	}
	moved := filepath.Join(p.QuarantineDir, q.Batch, strings.TrimPrefix(dir, "/"), "sub", "f1")
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("file not in quarantine at %s", moved)
	}
	bs, err := listBatches(p.QuarantineDir)
	if err != nil || len(bs) != 1 || bs[0].Count != 2 {
		t.Fatalf("batches: %+v %v", bs, err)
	}
	if !strings.Contains(log.String(), "quarantine "+filepath.Join(dir, "f2")) {
		t.Error("audit log should record the quarantine move")
	}

	// restore puts everything back and removes the batch
	n, skipped, err := restoreBatch(p.QuarantineDir, q.Batch, nil)
	if err != nil || n != 2 || len(skipped) != 0 {
		t.Fatalf("restore: n=%d skipped=%v err=%v", n, skipped, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "f1")); err != nil {
		t.Error("f1 not restored")
	}
	if bs, _ := listBatches(p.QuarantineDir); len(bs) != 0 {
		t.Error("batch dir should be gone after a full restore")
	}

	// restore refuses to clobber: quarantine again, recreate f2, restore
	q2, _ := openQuarantine(p.QuarantineDir, now.Add(time.Second), nil)
	x.Q = q2
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "f2"), 1)
	n, skipped, err = restoreBatch(p.QuarantineDir, q2.Batch, nil)
	if err != nil || n != 1 || len(skipped) != 1 {
		t.Fatalf("partial restore: n=%d skipped=%v err=%v", n, skipped, err)
	}
	if bs, _ := listBatches(p.QuarantineDir); len(bs) != 1 || bs[0].Count != 1 {
		t.Fatalf("batch should remain with 1 entry: %+v", bs)
	}

	// purge: an old batch goes, a fresh one stays; purge-all takes both
	oldName := now.Add(-10 * 24 * time.Hour).Format(batchLayout)
	oldDir := filepath.Join(p.QuarantineDir, oldName)
	write(t, filepath.Join(oldDir, "x", "f"), 10)
	write(t, filepath.Join(oldDir, "manifest.json"), 0)
	_ = os.WriteFile(filepath.Join(oldDir, "manifest.json"), []byte(`{"batch":"`+oldName+`","created":"`+now.Add(-10*24*time.Hour).Format(time.RFC3339)+`","entries":[{"from":"/x/f","to":"`+filepath.Join(oldDir, "x", "f")+`","bytes":10}]}`), 0o644)
	freed, names, err := purgeBatches(p.QuarantineDir, 7*24*time.Hour, now, false)
	if err != nil || len(names) != 1 || names[0] != oldName || freed != 10 {
		t.Fatalf("purge expired: freed=%d names=%v err=%v", freed, names, err)
	}
	if _, err := os.Stat(oldDir); err == nil {
		t.Error("old batch should be gone")
	}
	if bs, _ := listBatches(p.QuarantineDir); len(bs) != 1 {
		t.Error("fresh batch should survive an expiry purge")
	}
	_, names, err = purgeBatches(p.QuarantineDir, 7*24*time.Hour, now, true)
	if err != nil || len(names) != 1 {
		t.Fatalf("purge all: %v %v", names, err)
	}
	if bs, _ := listBatches(p.QuarantineDir); len(bs) != 0 {
		t.Error("purge all should empty the quarantine")
	}
}

func TestQuarantineMoveFailureLeavesSourceIntact(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(dir, "f"), 10)
	p := quarantinePolicy(home)
	q, _ := openQuarantine(p.QuarantineDir, time.Now(), func(src, dst string) error { return errors.New("EXDEV") })
	x := &Executor{Policy: p, Now: time.Now, Q: q}
	items := []PlanItem{{Entry: Entry{Path: dir, Action: ActionRmContents}, Bytes: 10, Deletable: 10}}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err) // per-item failure is reported, not fatal to the run
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Error("a failed move must leave the source in place")
	}
	if bs, _ := listBatches(p.QuarantineDir); len(bs) != 1 || bs[0].Count != 0 {
		t.Errorf("manifest must not record a failed move: %+v", bs)
	}
}

func TestDoCleanupQuarantinesAndReportsBatch(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(dir, "f"), 10)
	cfg := &Config{Version: 1, Volume: home, Policy: quarantinePolicy(home), home: home,
		KnownDirs: []Entry{{Path: dir, Type: "cache", Action: ActionRmContents}}}
	env := Env{Home: home, Procs: noProcs}
	var out, errw bytes.Buffer
	doCleanup(cfg, env, &opts{cleanup: true, yes: true}, time.Now(), &out, &errw)
	if _, err := os.Stat(filepath.Join(dir, "f")); err == nil {
		t.Fatal("file should have been moved")
	}
	if !strings.Contains(out.String(), "moved to quarantine batch") || !strings.Contains(out.String(), "--restore") {
		t.Errorf("output should name the batch and the undo: %s", out.String())
	}
	if bs, _ := listBatches(cfg.Policy.QuarantineDir); len(bs) != 1 {
		t.Error("expected one batch")
	}
}

func TestDiffReportsGrowth(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(dir, "f"), 1<<20)
	p := policyFor(home)
	st := &State{Known: map[string]int64{dir: 1024}}
	st.record("check", DiskUsage{Free: 10 << 30, Total: 20 << 30}, time.Now().Add(-time.Hour))
	if err := saveState(p.StateFile, st); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Version: 1, Volume: home, Policy: p, home: home,
		KnownDirs: []Entry{{Path: dir, Type: "cache", Action: ActionRmContents}}}
	var out, errw bytes.Buffer
	doDiff(cfg, Env{Home: home, Procs: noProcs}, &opts{diff: true}, time.Now(), &out, &errw)
	if !strings.Contains(out.String(), "+") || !strings.Contains(out.String(), dir) {
		t.Errorf("diff should show growth for %s: %s", dir, out.String())
	}
	got, _ := loadState(p.StateFile)
	if got.Known[dir] <= 1024 {
		t.Error("diff must record the new size")
	}
}

func TestNotifyOnlyWhenNotOK(t *testing.T) {
	home := t.TempDir()
	calls := 0
	old := notifyFn
	notifyFn = func(title, msg string) error { calls++; return nil }
	defer func() { notifyFn = old }()
	p := policyFor(home)
	p.MinFreeGB, p.WarnFreeGB = 0.000001, 0.000002 // any real disk is OK
	cfg := &Config{Version: 1, Volume: home, Policy: p, home: home}
	var out, errw bytes.Buffer
	doCheck(cfg, Env{Home: home}, &opts{check: true, quick: true, notify: true}, time.Now(), &out, &errw)
	if calls != 0 {
		t.Error("OK status must not notify")
	}
	p.MinFreeGB, p.WarnFreeGB = 1e9, 2e9 // any real disk is CRITICAL
	cfg.Policy = p
	code := doCheck(cfg, Env{Home: home}, &opts{check: true, quick: true, notify: true}, time.Now(), &out, &errw)
	if calls != 1 || code != exitCritical {
		t.Errorf("critical should notify once and exit 2: calls=%d code=%d", calls, code)
	}
}

func TestAgentFilesAndInstall(t *testing.T) {
	home := t.TempDir()
	files := agentFiles(home, "/usr/local/bin/oos")
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		if len(files) != 0 {
			t.Error("unsupported platform should return no files")
		}
		return
	}
	if len(files) == 0 {
		t.Fatal("expected agent files")
	}
	var ran [][]string
	run := func(name string, args ...string) error {
		ran = append(ran, append([]string{name}, args...))
		return nil
	}
	if err := agentInstall(home, "/usr/local/bin/oos", run); err != nil {
		t.Fatal(err)
	}
	for p, want := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("agent file not written: %s", p)
		}
		if string(b) != want || !strings.Contains(string(b), "/usr/local/bin/oos") {
			t.Errorf("agent file content mismatch for %s", p)
		}
		if !isUnder(p, home) {
			t.Errorf("agent file %s must live under home", p)
		}
	}
	if len(ran) == 0 {
		t.Error("install should invoke the service manager")
	}
	if err := agentUninstall(home, run); err != nil {
		t.Fatal(err)
	}
	for p := range files {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("agent file should be removed: %s", p)
		}
	}
}

func TestConfigNewValidation(t *testing.T) {
	base := `{"version":1,"volume":"/","policy":{"min_free_gb":1,"warn_free_gb":2,"max_delete_gb_per_run":1,"min_path_depth":3,"log_file":"~/l","state_file":"~/s","big_file_min_mb":1,"scan_top_n":1%s}%s}`
	cases := map[string]struct {
		policy, rest string
		ok           bool
	}{
		"stale needs hours":       {"", `,"known_dirs":[{"path":"~/a/b","type":"x","action":"rm-stale-children"}]`, false},
		"stale ok":                {"", `,"known_dirs":[{"path":"~/a/b","type":"x","action":"rm-stale-children","stale_after_hours":6}]`, true},
		"hours on wrong action":   {"", `,"known_dirs":[{"path":"~/a/b","type":"x","action":"rm-contents","stale_after_hours":6}]`, false},
		"quarantine needs dir":    {`,"quarantine":true,"quarantine_days":7`, "", false},
		"quarantine outside home": {`,"quarantine":true,"quarantine_dir":"/var/q","quarantine_days":7`, "", false},
		"quarantine ok":           {`,"quarantine":true,"quarantine_dir":"~/.q","quarantine_days":7`, "", true},
		"quarantine inside entry": {`,"quarantine":true,"quarantine_dir":"~/a/b/q","quarantine_days":7`, `,"known_dirs":[{"path":"~/a/b","type":"x","action":"rm-contents"}]`, false},
		"state inside entry":      {"", `,"known_dirs":[{"path":"~","type":"x","action":"rm-contents"}]`, false},
		"nested destructive":      {"", `,"known_dirs":[{"path":"~/a/b","type":"x","action":"rm-contents"},{"path":"~/a/b/c","type":"x","action":"rm-contents"}]`, false},
		"nested never is fine":    {"", `,"known_dirs":[{"path":"~/a/b","type":"x","action":"rm-contents"},{"path":"~/a/b/c","type":"x","action":"never"}]`, true},
	}
	for name, c := range cases {
		js := strings.Replace(strings.Replace(base, "%s", c.policy, 1), "%s", c.rest, 1)
		_, err := parseConfig([]byte(js), "/h")
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestBothDefaultConfigsValidate(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		cfg, err := parseConfig(defaultConfigFor(goos), "/Users/test")
		if err != nil {
			t.Errorf("%s default: %v", goos, err)
			continue
		}
		if !cfg.Policy.Quarantine {
			t.Errorf("%s default should quarantine", goos)
		}
		found := false
		for _, e := range cfg.KnownDirs {
			if e.Action == ActionRmStaleChilds {
				found = true
			}
		}
		if !found {
			t.Errorf("%s default should use rm-stale-children for the uv archive", goos)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	var out, errw bytes.Buffer
	if code := run([]string{"-V"}, &out, &errw); code != exitOK || !strings.Contains(out.String(), version) {
		t.Errorf("version flag: code=%d out=%q", code, out.String())
	}
}
