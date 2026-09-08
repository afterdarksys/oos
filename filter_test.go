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

func TestParseAge(t *testing.T) {
	day := 24 * time.Hour
	cases := map[string]time.Duration{
		"90d": 90 * day, "2w": 14 * day, "36h": 36 * time.Hour, "6mo": 180 * day, "1y": 365 * day,
		"30": 30 * day, "": 0, " 1.5d ": 36 * time.Hour, "0d": 0,
	}
	for in, want := range cases {
		got, err := parseAge(in)
		if err != nil || got != want {
			t.Errorf("parseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"x", "3 fortnights", "-1d", "1s", "d"} {
		if _, err := parseAge(bad); err == nil {
			t.Errorf("parseAge(%q) should fail", bad)
		}
	}
}

func TestExtOfAndTypeOf(t *testing.T) {
	cases := map[string]string{
		"a.tar.gz": "tar.gz", "b.TAR.XZ": "tar.xz", "syslog.log.1": "log", "app.log.2.gz": "log.gz",
		"x.dmg": "dmg", "README": "", ".bashrc": "", "dir.v2/file.ISO": "iso", "noext.": "",
	}
	for in, want := range cases {
		if got := extOf(in); got != want {
			t.Errorf("extOf(%q) = %q want %q", in, got, want)
		}
	}
	types := map[string]string{
		"x.dmg": "disk image", "y.tar.gz": "archive", "z.mkv": "video", "w.log.1": "log",
		"v.sqlite3": "database", "u.go": "source", "t.deb": "package", "s.safetensors": "model",
		"r.xyz": "other (.xyz)", "README": "no extension",
	}
	for in, want := range types {
		if got := typeOf(in, 0); got != want {
			t.Errorf("typeOf(%q) = %q want %q", in, got, want)
		}
	}
}

func TestMagicType(t *testing.T) {
	home := t.TempDir()
	put := func(name string, head []byte) string {
		p := filepath.Join(home, name)
		b := append(append([]byte{}, head...), bytes.Repeat([]byte{'x'}, 1<<20)...)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := map[string]string{
		"gz":     "archive",
		"pdf":    "document",
		"sqlite": "database",
		"elf":    "binary",
		"png":    "image",
		"text":   "text",
		"json":   "source",
	}
	heads := map[string][]byte{
		"gz": {0x1f, 0x8b, 8, 0}, "pdf": []byte("%PDF-1.7"), "sqlite": []byte("SQLite format 3\x00"),
		"elf": {0x7f, 'E', 'L', 'F'}, "png": {0x89, 'P', 'N', 'G'}, "text": []byte("hello there\n"), "json": []byte("{\"a\":1}\n"),
	}
	for name, want := range cases {
		p := put("blob-"+name, heads[name]) // no extension on purpose
		if got := typeOf(p, 2<<20); got != want {
			t.Errorf("%s: typeOf = %q want %q", name, got, want)
		}
	}
	// tiny files without an extension are not sniffed
	if got := typeOf(put("tiny", []byte("%PDF")), 4); got != "no extension" {
		t.Errorf("tiny file should not be sniffed, got %q", got)
	}
}

func TestFilterKeepAndSort(t *testing.T) {
	now := time.Now()
	f, err := filterFromOpts(&opts{olderThan: "30d", ext: "log, .dmg", sortBy: "oldest", top: 2})
	if err != nil {
		t.Fatal(err)
	}
	if f.keepTime(now.Add(-10*24*time.Hour), now) || !f.keepTime(now.Add(-40*24*time.Hour), now) {
		t.Error("older-than window wrong")
	}
	if !f.keepName("/a/b.log") || !f.keepName("/a/b.log.3") || !f.keepName("/a/x.DMG") || f.keepName("/a/b.txt") {
		t.Error("extension filter wrong")
	}
	if !f.keepName("/a/b.log.gz") {
		t.Error("gz suffix of a compound extension should match the plain extension")
	}
	if d := f.describe(); !strings.Contains(d, "older than 30d") || !strings.Contains(d, "ext dmg,log") || !strings.Contains(d, "sort oldest") {
		t.Errorf("describe: %q", d)
	}
	g, _ := filterFromOpts(&opts{newerThan: "2d"})
	if !g.keepTime(now.Add(-time.Hour), now) || g.keepTime(now.Add(-72*time.Hour), now) {
		t.Error("newer-than window wrong")
	}
	if _, err := filterFromOpts(&opts{sortBy: "sideways"}); err == nil {
		t.Error("bad sort must be rejected")
	}
	if _, err := filterFromOpts(&opts{olderThan: "soon"}); err == nil {
		t.Error("bad age must be rejected")
	}
	if capRows(10, 0, 4) != 4 || capRows(10, 7, 4) != 7 || capRows(3, 7, 4) != 3 || capRows(3, 0, 0) != 3 {
		t.Error("capRows")
	}
}

func TestByTypeWalk(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "a", "one.iso"), 4<<20)
	write(t, filepath.Join(home, "a", "two.iso"), 1<<20)
	write(t, filepath.Join(home, "b", "app.log"), 2<<20)
	write(t, filepath.Join(home, "b", "old.log"), 3<<20)
	age(t, filepath.Join(home, "b", "old.log"), 400*24*time.Hour)
	write(t, filepath.Join(home, "c", "notes.txt"), 1024)
	if err := os.Symlink(home, filepath.Join(home, "loop")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows, total, err := byType(home, rowFilter{sortBy: "size"}, now, 1)
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
	rows, _, _ = byType(home, rowFilter{olderThan: 365 * 24 * time.Hour}, now, 1)
	if len(rows) != 1 || rows[0].Type != "log" || rows[0].Files != 1 {
		t.Errorf("older-than should keep only old.log: %+v", rows)
	}
	// extension window
	rows, _, _ = byType(home, rowFilter{exts: map[string]bool{"iso": true}}, now, 1)
	if len(rows) != 1 || rows[0].Type != "disk image" {
		t.Errorf("ext should keep only isos: %+v", rows)
	}

	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home), home: home}
	var out, errw bytes.Buffer
	if code := doByType(cfg, Env{Home: home}, &opts{byType: home}, now, &out, &errw); code != exitOK {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "disk image") || !strings.Contains(out.String(), "one.iso") {
		t.Errorf("report:\n%s", out.String())
	}
	out.Reset()
	doByType(cfg, Env{Home: home}, &opts{byType: home, jsonOut: true, top: 1}, now, &out, &errw)
	var j struct {
		Types []TypeRow `json:"types"`
	}
	if err := json.Unmarshal(out.Bytes(), &j); err != nil || len(j.Types) != 1 || j.Types[0].Type != "disk image" {
		t.Errorf("json: %v %s", err, out.String())
	}
	if code := doByType(cfg, Env{Home: home}, &opts{byType: filepath.Join(home, "nope")}, now, &out, &errw); code != exitUsage {
		t.Error("missing dir must be a usage error")
	}
}

func TestScanFiltersSortAndCap(t *testing.T) {
	home := t.TempDir()
	p := policyFor(home)
	p.ScanTopN = 2
	cfg := &Config{Version: 1, Volume: home, Policy: p, home: home}
	write(t, filepath.Join(home, "d", "big-old.dmg"), 3<<20)
	age(t, filepath.Join(home, "d", "big-old.dmg"), 200*24*time.Hour)
	write(t, filepath.Join(home, "d", "mid-new.log"), 2<<20)
	write(t, filepath.Join(home, "d", "small-older.dmg"), 1<<20+1024)
	age(t, filepath.Join(home, "d", "small-older.dmg"), 300*24*time.Hour)
	now := time.Now()
	run := func(o *opts) string {
		o.scan = filepath.Join(home, "d")
		o.minMB = 1
		var out, errw bytes.Buffer
		if code := doScan(cfg, o, now, &out, &errw); code != exitOK {
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
	if code := doScan(cfg, &opts{scan: home, sortBy: "bogus"}, now, &out, &errw); code != exitUsage {
		t.Error("bad --sort must be a usage error")
	}
}

func TestAuditTagsAndFilters(t *testing.T) {
	home := t.TempDir()
	p := policyFor(home)
	cfg := &Config{Version: 1, Volume: home, Policy: p, home: home,
		KnownDirs: []Entry{{Path: filepath.Join(home, "tagged"), Type: "data", Action: ActionNever, Tags: []string{"review"}}}}
	write(t, filepath.Join(home, "old-cache", "f"), 2<<20)
	age(t, filepath.Join(home, "old-cache"), 400*24*time.Hour)
	age(t, filepath.Join(home, "old-cache", "f"), 400*24*time.Hour)
	write(t, filepath.Join(home, "fresh", "f"), 2<<20)
	write(t, filepath.Join(home, "tagged", "f"), 2<<20)
	write(t, filepath.Join(home, ".hidden-repo", ".git", "HEAD"), 2<<20)
	now := time.Now()
	audit := func(o *opts) []AuditRow {
		o.audit = home
		o.minMB = 1
		o.jsonOut = true
		var out, errw bytes.Buffer
		if code := doAudit(cfg, Env{Home: home}, o, now, &out, &errw); code != exitOK {
			t.Fatalf("exit %d: %s", code, errw.String())
		}
		var j struct {
			Rows []AuditRow `json:"rows"`
		}
		if err := json.Unmarshal(out.Bytes(), &j); err != nil {
			t.Fatal(err)
		}
		return j.Rows
	}
	byName := map[string][]string{}
	for _, r := range audit(&opts{}) {
		byName[filepath.Base(r.Path)] = r.Tags
	}
	if !hasString(byName["old-cache"], "stale-1y") || !hasString(byName["old-cache"], "build-output") {
		t.Errorf("old-cache tags: %v", byName["old-cache"])
	}
	if !hasString(byName[".hidden-repo"], "hidden") || !hasString(byName[".hidden-repo"], "repo") {
		t.Errorf(".hidden-repo tags: %v", byName[".hidden-repo"])
	}
	if !hasString(byName["tagged"], "review") || !hasString(byName["tagged"], "known") {
		t.Errorf("entry tags must show on the row: %v", byName["tagged"])
	}
	if hasString(byName["fresh"], "stale-30d") {
		t.Errorf("fresh must not be stale: %v", byName["fresh"])
	}
	rows := audit(&opts{tag: "stale-1y"})
	if len(rows) != 1 || filepath.Base(rows[0].Path) != "old-cache" {
		t.Errorf("--tag stale-1y: %+v", rows)
	}
	rows = audit(&opts{olderThan: "1y"})
	if len(rows) != 1 || filepath.Base(rows[0].Path) != "old-cache" {
		t.Errorf("--older-than 1y: %+v", rows)
	}
	rows = audit(&opts{sortBy: "oldest", top: 1})
	if len(rows) != 1 || filepath.Base(rows[0].Path) != "old-cache" {
		t.Errorf("--sort oldest --top 1: %+v", rows)
	}
	rows = audit(&opts{sortBy: "name"})
	if len(rows) < 2 || rows[0].Path > rows[1].Path {
		t.Errorf("name order: %+v", rows)
	}
	// text output carries the tags
	var out, errw bytes.Buffer
	doAudit(cfg, Env{Home: home}, &opts{audit: home, minMB: 1, tag: "review"}, now, &out, &errw)
	if !strings.Contains(out.String(), "[") || !strings.Contains(out.String(), "review") || strings.Contains(out.String(), "old-cache") {
		t.Errorf("text audit with --tag review:\n%s", out.String())
	}
}

func TestEntryTagsFilterAndAdd(t *testing.T) {
	home := t.TempDir()
	p := policyFor(home)
	write(t, filepath.Join(home, "c1", "f"), 1024)
	write(t, filepath.Join(home, "c2", "f"), 1024)
	cfg := &Config{Version: 1, Volume: home, Policy: p, home: home, KnownDirs: []Entry{
		{Path: filepath.Join(home, "c1"), Type: "cache", Action: ActionRmContents, Tags: []string{"Build-Output"}},
		{Path: filepath.Join(home, "c2"), Type: "cache", Action: ActionRmContents},
	}}
	if n := len(cfg.entriesTagged(nil, "build-output")); n != 1 {
		t.Errorf("tag filter is case-insensitive and exact: got %d", n)
	}
	if n := len(cfg.entriesTagged(nil, "")); n != 2 {
		t.Errorf("empty tag means all: got %d", n)
	}
	items := buildPlanTagged(cfg, Env{Home: home, Procs: noProcs}, nil, "build-output", time.Now())
	if len(items) != 1 || items[0].Path != filepath.Join(home, "c1") {
		t.Errorf("plan with tag: %+v", items)
	}
	var out, errw bytes.Buffer
	doCheck(cfg, Env{Home: home, Procs: noProcs}, &opts{check: true, tag: "build-output"}, time.Now(), &out, &errw)
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
	code := doAdd(Env{Home: home}, &opts{add: filepath.Join(home, "c2"), addType: "cache", addAction: ActionRmContents, addTags: "Review, build-output,review", config: cfgPath}, &out, &errw)
	if code != exitOK {
		t.Fatalf("add: %s", errw.String())
	}
	got, _, err := loadConfig(cfgPath, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.KnownDirs) != 1 || strings.Join(got.KnownDirs[0].Tags, ",") != "review,build-output" {
		t.Errorf("tags after --add: %+v", got.KnownDirs)
	}
	if splitTags(" ,, ") != nil {
		t.Error("empty tag list must be nil so it is omitted from the config")
	}
}
