package config

import (
	"strings"
	"testing"
)

func TestEmbeddedDefaultConfigIsValid(t *testing.T) {
	cfg, err := Parse(Default, "/Users/test")
	if err != nil {
		t.Fatalf("embedded default must validate: %v", err)
	}
	if len(cfg.KnownDirs) == 0 || len(cfg.KnownFiles) == 0 {
		t.Fatal("default config should list known dirs and files")
	}
	for _, e := range cfg.Entries(nil) {
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
		if _, err := Parse([]byte(js), "/h"); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestExpandHome(t *testing.T) {
	if got := ExpandHome("~/x/../y", "/h"); got != "/h/y" {
		t.Errorf("got %s", got)
	}
	if got := ExpandHome("~", "/h"); got != "/h" {
		t.Errorf("got %s", got)
	}
	if got := ExpandHome("/abs", "/h"); got != "/abs" {
		t.Errorf("got %s", got)
	}
}

// --- guards ---

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
		if got := IsUnder(c.p, c.base); got != c.want {
			t.Errorf("IsUnder(%q,%q)=%v want %v", c.p, c.base, got, c.want)
		}
	}
}
