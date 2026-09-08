package config

import (
	"strings"
	"testing"
)

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
		_, err := Parse([]byte(js), "/h")
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
		cfg, err := Parse(DefaultFor(goos), "/Users/test")
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
