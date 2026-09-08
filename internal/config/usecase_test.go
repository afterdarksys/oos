package config

import (
	"path/filepath"
	"strings"
	"testing"
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
		o, ok := cfg.OwnerFor(p)
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
		a := Attribute(p)
		if a.Label != want.label || a.Fingerprint != want.fp || a.Repo != want.repo {
			t.Errorf("Attribute(%s) = %+v, want %+v", p, a, want)
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
	if l, s := cfg.UseCaseFor(dir); l != "from entry" || s != "entry" {
		t.Errorf("entry should win: %s %s", l, s)
	}
	if l, s := cfg.UseCaseFor(filepath.Join(home, "proj", "src")); l != "from owner" || s != "owner" {
		t.Errorf("owner should be second: %s %s", l, s)
	}
	cfg.Policy.Owners = nil
	cfg.KnownDirs = nil
	if l, s := cfg.UseCaseFor(dir); l != "Rust build output" || s != "auto" {
		t.Errorf("auto should be last: %s %s", l, s)
	}
	if l, _ := cfg.UseCaseFor(filepath.Join(home, "nothing")); l != "" {
		t.Errorf("unknown should be empty, got %q", l)
	}
}

func TestOwnersValidation(t *testing.T) {
	base := `{"version":1,"volume":"/","policy":{"min_free_gb":1,"warn_free_gb":2,"max_delete_gb_per_run":1,"min_path_depth":3,"log_file":"~/l","state_file":"~/s","big_file_min_mb":1,"scan_top_n":1,"owners":[%s]}}`
	if _, err := Parse([]byte(strings.Replace(base, "%s", `{"match":"relative","use_case":"x"}`, 1)), "/h"); err == nil {
		t.Error("relative owner match must be rejected")
	}
	if _, err := Parse([]byte(strings.Replace(base, "%s", `{"match":"~/a","use_case":""}`, 1)), "/h"); err == nil {
		t.Error("empty use_case must be rejected")
	}
	cfg, err := Parse([]byte(strings.Replace(base, "%s", `{"match":"~/a/*/target","use_case":"rust"}`, 1)), "/h")
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
