package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// policyFor returns a permissive-enough policy rooted at home for tests.
func policyFor(home string) Policy {
	return Policy{
		MinFreeGB: 1, WarnFreeGB: 2, RequireYes: true, MaxDeleteGBPerRun: 1,
		AllowOutsideHome: false, AllowCommands: false,
		NeverTouch:   []string{filepath.Join(home, "keep")},
		MinPathDepth: 3,
		LogFile:      filepath.Join(home, "log"), StateFile: filepath.Join(home, "state.json"),
		BigFileMinMB: 1, ScanTopN: 10,
	}
}

func write(t *testing.T, p string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func noProcs() ([]string, error) { return nil, nil }

// --- config ---

func TestEmbeddedDefaultConfigIsValid(t *testing.T) {
	cfg, err := parseConfig(defaultConfig, "/Users/test")
	if err != nil {
		t.Fatalf("embedded default must validate: %v", err)
	}
	if len(cfg.KnownDirs) == 0 || len(cfg.KnownFiles) == 0 {
		t.Fatal("default config should list known dirs and files")
	}
	for _, e := range cfg.entries(nil) {
		if strings.HasPrefix(e.Path, "~") {
			t.Errorf("path not expanded: %s", e.Path)
		}
	}
	// The default must never allow deleting outside home.
	if cfg.Policy.AllowOutsideHome {
		t.Error("default allow_outside_home must be false")
	}
	if !cfg.Policy.RequireYes {
		t.Error("default require_yes must be true")
	}
}

func TestConfigRejectsBadShapes(t *testing.T) {
	cases := map[string]string{
		"unknown field":      `{"version":1,"volume":"/","policy":{"min_free_gb":1,"warn_free_gb":2,"max_delete_gb_per_run":1,"min_path_depth":3,"log_file":"/l","state_file":"/s","big_file_min_mb":1,"scan_top_n":1,"bogus":true}}`,
		"bad action":         `{"version":1,"volume":"/","policy":{"min_free_gb":1,"warn_free_gb":2,"max_delete_gb_per_run":1,"min_path_depth":3,"log_file":"/l","state_file":"/s","big_file_min_mb":1,"scan_top_n":1},"known_dirs":[{"path":"/a/b/c","type":"x","action":"nuke"}]}`,
		"rm on dir":          `{"version":1,"volume":"/","policy":{"min_free_gb":1,"warn_free_gb":2,"max_delete_gb_per_run":1,"min_path_depth":3,"log_file":"/l","state_file":"/s","big_file_min_mb":1,"scan_top_n":1},"known_dirs":[{"path":"/a/b/c","type":"x","action":"rm"}]}`,
		"command w/o cmd":    `{"version":1,"volume":"/","policy":{"min_free_gb":1,"warn_free_gb":2,"max_delete_gb_per_run":1,"min_path_depth":3,"log_file":"/l","state_file":"/s","big_file_min_mb":1,"scan_top_n":1},"known_dirs":[{"path":"/a/b/c","type":"x","action":"command"}]}`,
		"depth too shallow":  `{"version":1,"volume":"/","policy":{"min_free_gb":1,"warn_free_gb":2,"max_delete_gb_per_run":1,"min_path_depth":1,"log_file":"/l","state_file":"/s","big_file_min_mb":1,"scan_top_n":1}}`,
		"warn below min":     `{"version":1,"volume":"/","policy":{"min_free_gb":5,"warn_free_gb":2,"max_delete_gb_per_run":1,"min_path_depth":3,"log_file":"/l","state_file":"/s","big_file_min_mb":1,"scan_top_n":1}}`,
		"duplicate path":     `{"version":1,"volume":"/","policy":{"min_free_gb":1,"warn_free_gb":2,"max_delete_gb_per_run":1,"min_path_depth":3,"log_file":"/l","state_file":"/s","big_file_min_mb":1,"scan_top_n":1},"known_dirs":[{"path":"/a/b/c","type":"x","action":"never"},{"path":"/a/b/c","type":"x","action":"never"}]}`,
		"zero delete budget": `{"version":1,"volume":"/","policy":{"min_free_gb":1,"warn_free_gb":2,"max_delete_gb_per_run":0,"min_path_depth":3,"log_file":"/l","state_file":"/s","big_file_min_mb":1,"scan_top_n":1}}`,
	}
	for name, js := range cases {
		if _, err := parseConfig([]byte(js), "/h"); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestExpandHome(t *testing.T) {
	if got := expandHome("~/x/../y", "/h"); got != "/h/y" {
		t.Errorf("got %s", got)
	}
	if got := expandHome("~", "/h"); got != "/h" {
		t.Errorf("got %s", got)
	}
	if got := expandHome("/abs", "/h"); got != "/abs" {
		t.Errorf("got %s", got)
	}
}

// --- guards ---

func TestGuardsRefuse(t *testing.T) {
	home := t.TempDir()
	p := policyFor(home)
	env := Env{Home: home, Procs: noProcs}

	cacheDir := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(cacheDir, "f"), 10)
	link := filepath.Join(home, "a", "link")
	if err := os.Symlink(cacheDir, link); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(home, "keep", "sub")
	write(t, filepath.Join(keep, "f"), 10)
	outside := t.TempDir()

	cases := []struct {
		name string
		ent  Entry
		rule string
	}{
		{"never", Entry{Path: cacheDir, Action: ActionNever}, "action"},
		{"root", Entry{Path: "/", Action: ActionRmContents}, "root"},
		{"depth", Entry{Path: "/Users/x", Action: ActionRmContents}, "depth"},
		{"outside home", Entry{Path: outside, Action: ActionRmContents}, ""},
		{"never_touch", Entry{Path: keep, Action: ActionRmContents}, "never_touch"},
		{"symlink", Entry{Path: link, Action: ActionRmContents}, "symlink"},
		{"missing", Entry{Path: filepath.Join(home, "a", "nope"), Action: ActionRmContents}, "stat"},
		{"file with rm-contents", Entry{Path: filepath.Join(cacheDir, "f"), Action: ActionRmContents}, "kind"},
		{"dir with rm", Entry{Path: cacheDir, Action: ActionRm}, "kind"},
	}
	for _, c := range cases {
		err := env.checkDeletable(p, c.ent)
		if err == nil {
			t.Errorf("%s: expected refusal", c.name)
			continue
		}
		if c.rule != "" && !strings.HasPrefix(err.Error(), c.rule+":") {
			t.Errorf("%s: expected rule %q, got %v", c.name, c.rule, err)
		}
	}
	// The happy path must pass so we know the refusals above are not vacuous.
	if err := env.checkDeletable(p, Entry{Path: cacheDir, Action: ActionRmContents}); err != nil {
		t.Errorf("valid dir refused: %v", err)
	}
	// "/" in never_touch protects the root only; it must not swallow every path.
	p.NeverTouch = append(p.NeverTouch, "/")
	if err := env.checkDeletable(p, Entry{Path: cacheDir, Action: ActionRmContents}); err != nil {
		t.Errorf("never_touch '/' must not refuse a home path: %v", err)
	}
	if err := env.checkDeletable(p, Entry{Path: "/", Action: ActionRmContents}); err == nil {
		t.Error("root must still be refused")
	}
}

func TestIsUnder(t *testing.T) {
	cases := []struct {
		p, base string
		want    bool
	}{
		{"/a/b", "/a", true},
		{"/a", "/a", true},
		{"/ab", "/a", false},
		{"/a/b", "/", false},
		{"/", "/", true},
		{"/a/../b", "/b", true},
	}
	for _, c := range cases {
		if got := isUnder(c.p, c.base); got != c.want {
			t.Errorf("isUnder(%q,%q)=%v want %v", c.p, c.base, got, c.want)
		}
	}
}

func TestGuardProcesses(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "uv")
	write(t, filepath.Join(dir, "f"), 1)
	ent := Entry{Path: dir, Action: ActionRmContents, GuardProcesses: []string{"uvx"}}
	p := policyFor(home)

	busy := Env{Home: home, Procs: func() ([]string, error) { return []string{"/usr/local/bin/uv tool UVX serve"}, nil }}
	if err := busy.checkDeletable(p, ent); err == nil || !strings.HasPrefix(err.Error(), "processes:") {
		t.Errorf("expected process refusal, got %v", err)
	}
	idle := Env{Home: home, Procs: func() ([]string, error) { return []string{"bash", "vim"}, nil }}
	if err := idle.checkDeletable(p, ent); err != nil {
		t.Errorf("idle should pass: %v", err)
	}
	broken := Env{Home: home, Procs: func() ([]string, error) { return nil, os.ErrPermission }}
	if err := broken.checkDeletable(p, ent); err == nil {
		t.Error("process lister failure must refuse, not pass")
	}
	none := Env{Home: home, Procs: nil}
	if err := none.checkDeletable(p, ent); err == nil {
		t.Error("missing process lister with guards must refuse")
	}
}

// --- executor ---

func TestExecuteRmContentsKeepsDirAndDoesNotFollowSymlinks(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	target := filepath.Join(home, "a", "precious")
	write(t, filepath.Join(dir, "sub", "f1"), 100)
	write(t, filepath.Join(dir, "f2"), 50)
	write(t, filepath.Join(target, "t"), 7)
	if err := os.Symlink(target, filepath.Join(dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	// read-only tree, like a Go module cache
	ro := filepath.Join(dir, "ro", "pkg")
	write(t, filepath.Join(ro, "g"), 3)
	_ = os.Chmod(filepath.Join(ro, "g"), 0o444)
	_ = os.Chmod(ro, 0o555)

	var log bytes.Buffer
	x := &Executor{Policy: policyFor(home), Log: &log, Now: time.Now}
	items := []PlanItem{{Entry: Entry{Path: dir, Action: ActionRmContents}, Bytes: 160}}
	freed, err := x.Execute(items)
	if err != nil {
		t.Fatal(err)
	}
	if freed <= 0 {
		t.Error("expected bytes freed")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("dir itself must survive rm-contents")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("dir should be empty, has %d entries", len(ents))
	}
	if _, err := os.Stat(filepath.Join(target, "t")); err != nil {
		t.Error("symlink target must be untouched")
	}
	if !strings.Contains(log.String(), "rm-contents "+dir) {
		t.Error("audit log missing rm-contents line")
	}
}

func TestExecuteRefusesOverBudgetBeforeTouchingAnything(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(dir, "f"), 10)
	p := policyFor(home)
	p.MaxDeleteGBPerRun = 0.000001 // ~1 KB
	x := &Executor{Policy: p, Now: time.Now}
	items := []PlanItem{{Entry: Entry{Path: dir, Action: ActionRmContents}, Bytes: 5 * 1024, Deletable: 5 * 1024}}
	if _, err := x.Execute(items); err == nil || !strings.HasPrefix(err.Error(), "budget:") {
		t.Fatalf("expected budget refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Error("nothing may be deleted when the budget refuses the run")
	}
}

func TestExecuteSkipsRefusedAndHonoursAllowCommands(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(dir, "f"), 10)
	ran := 0
	p := policyFor(home)
	p.AllowCommands = false
	x := &Executor{Policy: p, Now: time.Now, Run: func(string) error { ran++; return nil }}
	items := []PlanItem{
		{Entry: Entry{Path: dir, Action: ActionRmContents}, Bytes: 10, Refused: refuse("test", "no")},
		{Entry: Entry{Path: "/x/y/z", Action: ActionCommand, Command: "true"}},
	}
	if _, err := x.Execute(items); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Error("refused item must not be deleted")
	}
	if ran != 0 {
		t.Error("command must not run when allow_commands is false")
	}
	p.AllowCommands = true
	x.Policy = p
	if _, err := x.Execute(items[1:]); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Errorf("command should run once, ran %d", ran)
	}
}

func TestCleanupDryRunByDefault(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(dir, "f"), 10)
	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home),
		KnownDirs: []Entry{{Path: dir, Type: "cache", Action: ActionRmContents}}}
	env := Env{Home: home, Procs: noProcs}
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
	write(t, filepath.Join(dir, "f"), 8192)
	big := filepath.Join(home, "big")
	write(t, filepath.Join(big, "b"), 1<<20)
	if err := os.Symlink(big, filepath.Join(dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	n, err := pathSize(dir)
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

func TestScanBigSortedAndCapped(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "s", "a"), 3<<20)
	write(t, filepath.Join(home, "s", "b"), 5<<20)
	write(t, filepath.Join(home, "s", "c"), 1<<20)
	write(t, filepath.Join(home, "s", "tiny"), 10)
	hits, err := scanBig(filepath.Join(home, "s"), 1<<20, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("want 2 hits, got %d", len(hits))
	}
	if filepath.Base(hits[0].Path) != "b" || filepath.Base(hits[1].Path) != "a" {
		t.Errorf("wrong order: %v", hits)
	}
}

// --- state and flags ---

func TestStateRoundTripAndCorruptRecovery(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "st", "bigfile.json")
	s := &State{Known: map[string]int64{"/a": 1}}
	s.record("check", DiskUsage{Free: 1 << 30, Total: 2 << 30}, time.Now())
	if err := saveState(p, s); err != nil {
		t.Fatal(err)
	}
	got, err := loadState(p)
	if err != nil || got.Known["/a"] != 1 || len(got.History) != 1 {
		t.Fatalf("round trip failed: %v %+v", err, got)
	}
	_ = os.WriteFile(p, []byte("{not json"), 0o644)
	got, err = loadState(p)
	if err != nil || got == nil {
		t.Fatal("corrupt state must yield a fresh state, not an error")
	}
	m, _ := filepath.Glob(p + ".corrupt-*")
	if len(m) != 1 {
		t.Error("corrupt state should be preserved under a .corrupt- name")
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
