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

func TestWhyVerdicts(t *testing.T) {
	home := t.TempDir()
	known := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(known, "f"), 10)
	never := filepath.Join(home, "a", "keepme")
	write(t, filepath.Join(never, "f"), 10)
	unknownDir := filepath.Join(home, "a", "mystery")
	write(t, filepath.Join(unknownDir, "f"), 10)
	protected := filepath.Join(home, "keep", "x")
	write(t, filepath.Join(protected, "f"), 10)
	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home), home: home,
		KnownDirs: []Entry{
			{Path: known, Type: "cache", Action: ActionRmContents},
			{Path: never, Type: "data", Action: ActionNever},
		}}
	env := Env{Home: home, Procs: noProcs}
	cases := map[string]struct {
		verdict string
		code    int
	}{
		known:                       {"would-act", exitOK},
		never:                       {"never", exitCritical},
		unknownDir:                  {"unknown-allowed", exitWarn},
		protected:                   {"unknown-refused", exitCritical},
		filepath.Join(known, "f"):   {"unknown-allowed", exitWarn}, // under an entry, judged on its own
		filepath.Join(never, "f"):   {"never", exitCritical},       // a never parent protects its children
		filepath.Join(home, "nope"): {"unknown-refused", exitCritical},
	}
	for p, want := range cases {
		var out, errw bytes.Buffer
		code := doWhy(cfg, env, &opts{why: p, jsonOut: true, quick: true}, &out, &errw)
		var r whyResult
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("%s: bad json %q", p, out.String())
		}
		if r.Verdict != want.verdict || code != want.code {
			t.Errorf("%s: verdict=%s code=%d, want %s %d (reasons %v)", p, r.Verdict, code, want.verdict, want.code, r.Reasons)
		}
	}
	// text form mentions the guard
	var out, errw bytes.Buffer
	doWhy(cfg, env, &opts{why: protected, quick: true}, &out, &errw)
	if !strings.Contains(out.String(), "never_touch") {
		t.Errorf("text form should name the guard: %s", out.String())
	}
}

func TestEnsureAlreadySatisfiedAndPlanOnly(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(dir, "f"), 1<<20)
	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home), home: home,
		KnownDirs: []Entry{{Path: dir, Type: "cache", Action: ActionRmContents}}}
	env := Env{Home: home, Procs: noProcs}
	var out, errw bytes.Buffer
	// any real disk has more than a millionth of a GB free
	if code := doEnsure(cfg, env, &opts{ensure: 0.000001}, time.Now(), &out, &errw); code != exitOK || !strings.Contains(out.String(), "already") {
		t.Errorf("satisfied ensure: code=%d out=%s", code, out.String())
	}
	// an impossible target plans everything and reports short, without touching
	out.Reset()
	code := doEnsure(cfg, env, &opts{ensure: 1e9, jsonOut: true}, time.Now(), &out, &errw)
	var doc map[string]any
	_ = json.Unmarshal(out.Bytes(), &doc)
	if code != exitCritical || doc["reached"] != false || doc["live"] != false {
		t.Errorf("impossible ensure: code=%d doc=%v", code, doc)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Error("plan-only ensure must not delete")
	}
	steps, _ := doc["steps"].([]any)
	if len(steps) == 0 || !strings.Contains(steps[0].(string), dir) {
		t.Errorf("plan should list the cache entry first: %v", steps)
	}
}

func TestEnsureLiveDeletesUntilTargetOrExhausted(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(dir, "f"), 1<<20)
	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home), home: home,
		KnownDirs: []Entry{{Path: dir, Type: "cache", Action: ActionRmContents}}}
	env := Env{Home: home, Procs: noProcs}
	var out, errw bytes.Buffer
	code := doEnsure(cfg, env, &opts{ensure: 1e9, yes: true}, time.Now(), &out, &errw)
	if code != exitCritical {
		t.Errorf("unreachable target must exit 2, got %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err == nil {
		t.Error("live ensure should have removed the cache contents trying to reach the target")
	}
	if _, err := os.Stat(cfg.Policy.LogFile); err != nil {
		t.Error("live ensure must write the audit log")
	}
}

func TestAddAndForgetRewriteConfigSafely(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "oos.json")
	if err := os.WriteFile(cfgPath, defaultConfigFor("darwin"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "proj", "target")
	write(t, filepath.Join(target, "x"), 1)
	env := Env{Home: home}
	var out, errw bytes.Buffer
	o := &opts{config: cfgPath, add: target, addType: "build", addAction: ActionRmContents, addNote: "rust target"}
	if code := doAdd(env, o, &out, &errw); code != exitOK {
		t.Fatalf("add: %s", errw.String())
	}
	cfg, _, err := loadConfig(cfgPath, home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range cfg.KnownDirs {
		if e.Path == target && e.Type == "build" && e.Note == "rust target" {
			found = true
		}
	}
	if !found {
		t.Error("added entry not found after reload")
	}
	if !strings.Contains(string(mustRead(t, cfgPath)), `"~/proj/target"`) {
		t.Error("paths under home should be stored with ~")
	}
	// duplicate add refused
	errw.Reset()
	if code := doAdd(env, o, &out, &errw); code == exitOK {
		t.Error("duplicate add must be refused")
	}
	// invalid add refused and file untouched
	before := mustRead(t, cfgPath)
	bad := &opts{config: cfgPath, add: filepath.Join(home, "other"), addType: "x", addAction: "nuke"}
	if code := doAdd(env, bad, &out, &errw); code == exitOK {
		t.Error("invalid action must be refused")
	}
	if string(before) != string(mustRead(t, cfgPath)) {
		t.Error("a refused add must not modify the file")
	}
	// forget
	if code := doForget(env, &opts{config: cfgPath, forget: target}, &out, &errw); code != exitOK {
		t.Fatalf("forget: %s", errw.String())
	}
	cfg, _, _ = loadConfig(cfgPath, home)
	for _, e := range cfg.KnownDirs {
		if e.Path == target {
			t.Error("entry should be gone after forget")
		}
	}
	if code := doForget(env, &opts{config: cfgPath, forget: target}, &out, &errw); code == exitOK {
		t.Error("forgetting a missing entry must fail")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOverridesAndQuietAndFree(t *testing.T) {
	home := t.TempDir()
	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home), home: home}
	o := &opts{warnGB: 5, critGB: 10}
	if err := applyOverrides(cfg, o); err == nil {
		t.Error("warn below critical must be rejected")
	}
	o = &opts{warnGB: 1e9, critGB: 1e8}
	if err := applyOverrides(cfg, o); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	code := doFree(cfg, &opts{}, &out, &errw)
	if code != exitCritical {
		t.Errorf("with a 1e8 GB critical line any disk is critical, got %d", code)
	}
	if strings.TrimSpace(out.String()) == "" || strings.Contains(out.String(), ".") {
		t.Errorf("--free should print a bare integer, got %q", out.String())
	}
}

func TestLogTail(t *testing.T) {
	home := t.TempDir()
	p := policyFor(home)
	if err := os.MkdirAll(filepath.Dir(p.LogFile), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(p.LogFile, []byte("a\nb\nc\nd\n"), 0o644)
	cfg := &Config{Version: 1, Volume: home, Policy: p, home: home}
	var out, errw bytes.Buffer
	doLogTail(cfg, &opts{logTail: 2}, &out, &errw)
	if out.String() != "c\nd\n" {
		t.Errorf("tail 2 = %q", out.String())
	}
}

func TestCleanupJSONPlan(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	write(t, filepath.Join(dir, "f"), 10)
	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home), home: home,
		KnownDirs: []Entry{{Path: dir, Type: "cache", Action: ActionRmContents}}}
	var out, errw bytes.Buffer
	doCleanup(cfg, Env{Home: home, Procs: noProcs}, &opts{cleanup: true, jsonOut: true}, time.Now(), &out, &errw)
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("not json: %q", out.String())
	}
	if doc["live"] != false || doc["plan"] == nil {
		t.Errorf("plan json: %v", doc)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Error("json dry-run must not delete")
	}
}

func TestNewFlagsParse(t *testing.T) {
	var errw bytes.Buffer
	o, err := parseFlags([]string{"-E", "20", "-Q", "--warn", "80", "--critical", "30", "-W", "/x"}, &errw)
	if err != nil {
		t.Fatal(err)
	}
	if o.ensure != 20 || !o.quiet || o.warnGB != 80 || o.critGB != 30 || o.why != "/x" {
		t.Errorf("flags: %+v", o)
	}
	o, err = parseFlags([]string{"--add", "/p", "--type", "cache", "--action", "rm-contents", "--stale-hours", "6", "--log-tail", "5", "-F"}, &errw)
	if err != nil || o.add != "/p" || o.addType != "cache" || o.addStale != 6 || o.logTail != 5 || !o.free {
		t.Errorf("flags: %+v %v", o, err)
	}
}
