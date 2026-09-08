package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/audit"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestFilterKeepAndSort(t *testing.T) {
	now := time.Now()
	f, err := filterFromOpts(&opts{olderThan: "30d", ext: "log, .dmg", sortBy: "oldest", top: 2})
	if err != nil {
		t.Fatal(err)
	}
	if f.KeepTime(now.Add(-10*24*time.Hour), now) || !f.KeepTime(now.Add(-40*24*time.Hour), now) {
		t.Error("older-than window wrong")
	}
	if !f.KeepName("/a/b.log") || !f.KeepName("/a/b.log.3") || !f.KeepName("/a/x.DMG") || f.KeepName("/a/b.txt") {
		t.Error("extension filter wrong")
	}
	if !f.KeepName("/a/b.log.gz") {
		t.Error("gz suffix of a compound extension should match the plain extension")
	}
	if d := f.Describe(); !strings.Contains(d, "older than 30d") || !strings.Contains(d, "ext dmg,log") || !strings.Contains(d, "sort oldest") {
		t.Errorf("describe: %q", d)
	}
	g, _ := filterFromOpts(&opts{newerThan: "2d"})
	if !g.KeepTime(now.Add(-time.Hour), now) || g.KeepTime(now.Add(-72*time.Hour), now) {
		t.Error("newer-than window wrong")
	}
	if _, err := filterFromOpts(&opts{sortBy: "sideways"}); err == nil {
		t.Error("bad sort must be rejected")
	}
	if _, err := filterFromOpts(&opts{olderThan: "soon"}); err == nil {
		t.Error("bad age must be rejected")
	}
	if size.CapRows(10, 0, 4) != 4 || size.CapRows(10, 7, 4) != 7 || size.CapRows(3, 7, 4) != 3 || size.CapRows(3, 0, 0) != 3 {
		t.Error("capRows")
	}
}

func TestByTypeWalk(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, filepath.Join(home, "a", "one.iso"), 4<<20)
	testutil.Write(t, filepath.Join(home, "a", "two.iso"), 1<<20)
	testutil.Write(t, filepath.Join(home, "b", "app.log"), 2<<20)
	testutil.Write(t, filepath.Join(home, "b", "old.log"), 3<<20)
	testutil.Age(t, filepath.Join(home, "b", "old.log"), 400*24*time.Hour)
	testutil.Write(t, filepath.Join(home, "c", "notes.txt"), 1024)
	if err := os.Symlink(home, filepath.Join(home, "loop")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows, total, err := size.ByType(home, size.Filter{SortBy: "size"}, now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Type != "disk image" || rows[0].Files != 2 || rows[1].Type != "log" || rows[2].Type != "document" {
		t.Fatalf("rows: %+v", rows)
	}
	if rows[0].Largest[0].Path != filepath.Join(home, "a", "one.iso") || len(rows[0].Largest) != 1 {
		t.Errorf("largest: %+v", rows[0].Largest)
	}
	if total < 10<<20 {
		t.Errorf("total %d", total)
	}
	// age window
	rows, _, _ = size.ByType(home, size.Filter{OlderThan: 365 * 24 * time.Hour}, now, 1)
	if len(rows) != 1 || rows[0].Type != "log" || rows[0].Files != 1 {
		t.Errorf("older-than should keep only old.log: %+v", rows)
	}
	// extension window
	rows, _, _ = size.ByType(home, size.Filter{Exts: map[string]bool{"iso": true}}, now, 1)
	if len(rows) != 1 || rows[0].Type != "disk image" {
		t.Errorf("ext should keep only isos: %+v", rows)
	}

	cfg := &config.Config{Version: 1, Volume: home, Policy: testutil.PolicyFor(home), Home: home}
	var out, errw bytes.Buffer
	if code := doByType(cfg, guard.Env{Home: home}, &opts{byType: home}, now, &out, &errw); code != status.ExitOK {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "disk image") || !strings.Contains(out.String(), "one.iso") {
		t.Errorf("report:\n%s", out.String())
	}
	out.Reset()
	doByType(cfg, guard.Env{Home: home}, &opts{byType: home, jsonOut: true, top: 1}, now, &out, &errw)
	var j struct {
		Types []size.TypeRow `json:"types"`
	}
	if err := json.Unmarshal(out.Bytes(), &j); err != nil || len(j.Types) != 1 || j.Types[0].Type != "disk image" {
		t.Errorf("json: %v %s", err, out.String())
	}
	if code := doByType(cfg, guard.Env{Home: home}, &opts{byType: filepath.Join(home, "nope")}, now, &out, &errw); code != status.ExitUsage {
		t.Error("missing dir must be a usage error")
	}
}

func TestScanFiltersSortAndCap(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	p.ScanTopN = 2
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	testutil.Write(t, filepath.Join(home, "d", "big-old.dmg"), 3<<20)
	testutil.Age(t, filepath.Join(home, "d", "big-old.dmg"), 200*24*time.Hour)
	testutil.Write(t, filepath.Join(home, "d", "mid-new.log"), 2<<20)
	testutil.Write(t, filepath.Join(home, "d", "small-older.dmg"), 1<<20+1024)
	testutil.Age(t, filepath.Join(home, "d", "small-older.dmg"), 300*24*time.Hour)
	now := time.Now()
	run := func(o *opts) string {
		o.scan = filepath.Join(home, "d")
		o.minMB = 1
		var out, errw bytes.Buffer
		if code := doScan(cfg, o, now, &out, &errw); code != status.ExitOK {
			t.Fatalf("exit %d: %s", code, errw.String())
		}
		return out.String()
	}
	s := run(&opts{})
	if !strings.Contains(s, "2 shown") || strings.Contains(s, "small-older") {
		t.Errorf("default cap is scan_top_n:\n%s", s)
	}
	s = run(&opts{top: 10, sortBy: "oldest"})
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) < 4 || !strings.Contains(lines[1], "small-older") || !strings.Contains(lines[3], "mid-new") {
		t.Errorf("oldest first:\n%s", s)
	}
	s = run(&opts{top: 10, ext: "dmg", olderThan: "250d"})
	if strings.Contains(s, "mid-new") || strings.Contains(s, "big-old") || !strings.Contains(s, "small-older") {
		t.Errorf("ext + older-than:\n%s", s)
	}
	s = run(&opts{top: 10, sortBy: "name"})
	lines = strings.Split(strings.TrimSpace(s), "\n")
	if !strings.Contains(lines[1], "big-old") || !strings.Contains(lines[2], "mid-new") {
		t.Errorf("name order:\n%s", s)
	}
	var errw, out bytes.Buffer
	if code := doScan(cfg, &opts{scan: home, sortBy: "bogus"}, now, &out, &errw); code != status.ExitUsage {
		t.Error("bad --sort must be a usage error")
	}
}

func TestAuditTagsAndFilters(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home,
		KnownDirs: []config.Entry{{Path: filepath.Join(home, "tagged"), Type: "data", Action: config.ActionNever, Tags: []string{"review"}}}}
	testutil.Write(t, filepath.Join(home, "old-cache", "f"), 2<<20)
	testutil.Age(t, filepath.Join(home, "old-cache"), 400*24*time.Hour)
	testutil.Age(t, filepath.Join(home, "old-cache", "f"), 400*24*time.Hour)
	testutil.Write(t, filepath.Join(home, "fresh", "f"), 2<<20)
	testutil.Write(t, filepath.Join(home, "tagged", "f"), 2<<20)
	testutil.Write(t, filepath.Join(home, ".hidden-repo", ".git", "HEAD"), 2<<20)
	now := time.Now()
	auditRows := func(o *opts) []audit.Row {
		o.audit = home
		o.minMB = 1
		o.jsonOut = true
		var out, errw bytes.Buffer
		if code := doAudit(cfg, guard.Env{Home: home}, o, now, &out, &errw); code != status.ExitOK {
			t.Fatalf("exit %d: %s", code, errw.String())
		}
		var j struct {
			Rows []audit.Row `json:"rows"`
		}
		if err := json.Unmarshal(out.Bytes(), &j); err != nil {
			t.Fatal(err)
		}
		return j.Rows
	}
	byName := map[string][]string{}
	for _, r := range auditRows(&opts{}) {
		byName[filepath.Base(r.Path)] = r.Tags
	}
	if !audit.HasString(byName["old-cache"], "stale-1y") || !audit.HasString(byName["old-cache"], "build-output") {
		t.Errorf("old-cache tags: %v", byName["old-cache"])
	}
	if !audit.HasString(byName[".hidden-repo"], "hidden") || !audit.HasString(byName[".hidden-repo"], "repo") {
		t.Errorf(".hidden-repo tags: %v", byName[".hidden-repo"])
	}
	if !audit.HasString(byName["tagged"], "review") || !audit.HasString(byName["tagged"], "known") {
		t.Errorf("entry tags must show on the row: %v", byName["tagged"])
	}
	if audit.HasString(byName["fresh"], "stale-30d") {
		t.Errorf("fresh must not be stale: %v", byName["fresh"])
	}
	rows := auditRows(&opts{tag: "stale-1y"})
	if len(rows) != 1 || filepath.Base(rows[0].Path) != "old-cache" {
		t.Errorf("--tag stale-1y: %+v", rows)
	}
	rows = auditRows(&opts{olderThan: "1y"})
	if len(rows) != 1 || filepath.Base(rows[0].Path) != "old-cache" {
		t.Errorf("--older-than 1y: %+v", rows)
	}
	rows = auditRows(&opts{sortBy: "oldest", top: 1})
	if len(rows) != 1 || filepath.Base(rows[0].Path) != "old-cache" {
		t.Errorf("--sort oldest --top 1: %+v", rows)
	}
	rows = auditRows(&opts{sortBy: "name"})
	if len(rows) < 2 || rows[0].Path > rows[1].Path {
		t.Errorf("name order: %+v", rows)
	}
	// text output carries the tags
	var out, errw bytes.Buffer
	doAudit(cfg, guard.Env{Home: home}, &opts{audit: home, minMB: 1, tag: "review"}, now, &out, &errw)
	if !strings.Contains(out.String(), "[") || !strings.Contains(out.String(), "review") || strings.Contains(out.String(), "old-cache") {
		t.Errorf("text audit with --tag review:\n%s", out.String())
	}
}

func TestEntryTagsFilterAndAdd(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	testutil.Write(t, filepath.Join(home, "c1", "f"), 1024)
	testutil.Write(t, filepath.Join(home, "c2", "f"), 1024)
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home, KnownDirs: []config.Entry{
		{Path: filepath.Join(home, "c1"), Type: "cache", Action: config.ActionRmContents, Tags: []string{"Build-Output"}},
		{Path: filepath.Join(home, "c2"), Type: "cache", Action: config.ActionRmContents},
	}}
	if n := len(cfg.EntriesTagged(nil, "build-output")); n != 1 {
		t.Errorf("tag filter is case-insensitive and exact: got %d", n)
	}
	if n := len(cfg.EntriesTagged(nil, "")); n != 2 {
		t.Errorf("empty tag means all: got %d", n)
	}
	items := plan.BuildTagged(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, nil, "build-output", time.Now())
	if len(items) != 1 || items[0].Path != filepath.Join(home, "c1") {
		t.Errorf("plan with tag: %+v", items)
	}
	var out, errw bytes.Buffer
	doCheck(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, &opts{check: true, tag: "build-output"}, time.Now(), &out, &errw)
	if !strings.Contains(out.String(), "known entries (1") {
		t.Errorf("check honours --tag:\n%s", out.String())
	}
	out.Reset()
	doShow(cfg, "test", &opts{}, &out)
	if !strings.Contains(out.String(), "[Build-Output]") {
		t.Errorf("show prints tags:\n%s", out.String())
	}

	// --add --tags writes them, lowercase and deduplicated, and the config reloads
	cfgPath := filepath.Join(home, "oos.json")
	if err := os.WriteFile(cfgPath, []byte(`{"version":1,"volume":"~","policy":{"min_free_gb":1,"warn_free_gb":2,"require_yes":true,"max_delete_gb_per_run":1,"min_path_depth":3,"log_file":"~/l","state_file":"~/s","big_file_min_mb":1,"scan_top_n":1},"known_dirs":[],"known_files":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code := doAdd(guard.Env{Home: home}, &opts{add: filepath.Join(home, "c2"), addType: "cache", addAction: config.ActionRmContents, addTags: "Review, build-output,review", config: cfgPath}, &out, &errw)
	if code != status.ExitOK {
		t.Fatalf("add: %s", errw.String())
	}
	got, _, err := config.Load(cfgPath, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.KnownDirs) != 1 || strings.Join(got.KnownDirs[0].Tags, ",") != "review,build-output" {
		t.Errorf("tags after --add: %+v", got.KnownDirs)
	}
	if config.SplitTags(" ,, ") != nil {
		t.Error("empty tag list must be nil so it is omitted from the config")
	}
}
