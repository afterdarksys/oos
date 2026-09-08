package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed oos.json
var defaultConfigDarwin []byte

//go:embed oos.linux.json
var defaultConfigLinux []byte

// defaultConfigFor picks the embedded default for a platform. macOS is the
// fallback because that is where oos was born.
func defaultConfigFor(goos string) []byte {
	if goos == "linux" {
		return defaultConfigLinux
	}
	return defaultConfigDarwin
}

var defaultConfig = defaultConfigFor(runtime.GOOS)

// Config is the on-disk oos.json shape.
type Config struct {
	Version    int     `json:"version"`
	Volume     string  `json:"volume"`
	Policy     Policy  `json:"policy"`
	KnownDirs  []Entry `json:"known_dirs"`
	KnownFiles []Entry `json:"known_files"`

	home string
}

// Policy holds the cover-your-ass controls. Every field is a limit, never a permission grant.
type Policy struct {
	MinFreeGB         float64  `json:"min_free_gb"`
	WarnFreeGB        float64  `json:"warn_free_gb"`
	RequireYes        bool     `json:"require_yes"`
	MaxDeleteGBPerRun float64  `json:"max_delete_gb_per_run"`
	AllowOutsideHome  bool     `json:"allow_outside_home"`
	AllowCommands     bool     `json:"allow_commands"`
	NeverTouch        []string `json:"never_touch"`
	MinPathDepth      int      `json:"min_path_depth"`
	LogFile           string   `json:"log_file"`
	StateFile         string   `json:"state_file"`
	BigFileMinMB      int64    `json:"big_file_min_mb"`
	ScanTopN          int      `json:"scan_top_n"`

	// Quarantine: when true, rm actions move into QuarantineDir/<batch>/ instead
	// of deleting. Space comes back on --purge (batches older than
	// QuarantineDays) or --purge-now.
	Quarantine     bool   `json:"quarantine"`
	QuarantineDir  string `json:"quarantine_dir"`
	QuarantineDays int    `json:"quarantine_days"`
}

// Entry is one known large directory or file.
type Entry struct {
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	Action         string   `json:"action"`
	Command        string   `json:"command,omitempty"`
	GuardProcesses []string `json:"guard_processes,omitempty"`
	Note           string   `json:"note,omitempty"`

	// StaleAfterHours applies to rm-stale-children: a child modified more
	// recently than this is kept regardless of references.
	StaleAfterHours int `json:"stale_after_hours,omitempty"`

	// IsFile is set by the loader: true for known_files entries.
	IsFile bool `json:"-"`
}

const (
	ActionNever         = "never"
	ActionRmContents    = "rm-contents"
	ActionRm            = "rm"
	ActionCommand       = "command"
	ActionRmStaleChilds = "rm-stale-children"
)

var validActions = map[string]bool{
	ActionNever: true, ActionRmContents: true, ActionRm: true, ActionCommand: true, ActionRmStaleChilds: true,
}

func isDestructive(action string) bool {
	return action == ActionRm || action == ActionRmContents || action == ActionRmStaleChilds
}

// expandHome replaces a leading "~" with home and cleans the path.
func expandHome(p, home string) string {
	if p == "~" {
		return filepath.Clean(home)
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Clean(filepath.Join(home, p[2:]))
	}
	return filepath.Clean(p)
}

// configCandidates is the lookup order when --config is not given.
func configCandidates(home string) []string {
	return []string{
		"oos.json",
		filepath.Join(home, ".config", "oos", "oos.json"),
	}
}

// loadConfig reads the first config found, or the embedded default.
// It returns the config, the source it came from, and any error.
func loadConfig(explicit, home string) (*Config, string, error) {
	if explicit != "" {
		b, err := os.ReadFile(explicit)
		if err != nil {
			return nil, "", fmt.Errorf("read config %s: %w", explicit, err)
		}
		cfg, err := parseConfig(b, home)
		return cfg, explicit, err
	}
	for _, c := range configCandidates(home) {
		b, err := os.ReadFile(c)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, "", fmt.Errorf("read config %s: %w", c, err)
		}
		cfg, err := parseConfig(b, home)
		return cfg, c, err
	}
	cfg, err := parseConfig(defaultConfig, home)
	return cfg, "embedded default", err
}

// parseConfig decodes, expands ~ and validates. Unknown fields are an error:
// a typo in a policy key must not silently weaken the policy.
func parseConfig(b []byte, home string) (*Config, error) {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.home = filepath.Clean(home)
	cfg.expand(home)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) expand(home string) {
	c.Volume = expandHome(c.Volume, home)
	c.Policy.LogFile = expandHome(c.Policy.LogFile, home)
	c.Policy.StateFile = expandHome(c.Policy.StateFile, home)
	if c.Policy.QuarantineDir != "" {
		c.Policy.QuarantineDir = expandHome(c.Policy.QuarantineDir, home)
	}
	for i := range c.Policy.NeverTouch {
		c.Policy.NeverTouch[i] = expandHome(c.Policy.NeverTouch[i], home)
	}
	for i := range c.KnownDirs {
		c.KnownDirs[i].Path = expandHome(c.KnownDirs[i].Path, home)
		c.KnownDirs[i].IsFile = false
	}
	for i := range c.KnownFiles {
		c.KnownFiles[i].Path = expandHome(c.KnownFiles[i].Path, home)
		c.KnownFiles[i].IsFile = true
	}
}

func (c *Config) validate() error {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }

	if c.Version != 1 {
		add("version must be 1, got %d", c.Version)
	}
	if !filepath.IsAbs(c.Volume) {
		add("volume must be an absolute path, got %q", c.Volume)
	}
	p := c.Policy
	if p.MinFreeGB <= 0 {
		add("policy.min_free_gb must be > 0")
	}
	if p.WarnFreeGB < p.MinFreeGB {
		add("policy.warn_free_gb (%.0f) must be >= min_free_gb (%.0f)", p.WarnFreeGB, p.MinFreeGB)
	}
	if p.MaxDeleteGBPerRun <= 0 {
		add("policy.max_delete_gb_per_run must be > 0")
	}
	if p.MinPathDepth < 2 {
		add("policy.min_path_depth must be >= 2 (got %d); depth 1 is a top-level dir like /Users", p.MinPathDepth)
	}
	if !filepath.IsAbs(p.LogFile) {
		add("policy.log_file must be absolute after ~ expansion")
	}
	if !filepath.IsAbs(p.StateFile) {
		add("policy.state_file must be absolute after ~ expansion")
	}
	if p.BigFileMinMB <= 0 {
		add("policy.big_file_min_mb must be > 0")
	}
	if p.ScanTopN <= 0 {
		add("policy.scan_top_n must be > 0")
	}
	for _, nt := range p.NeverTouch {
		if !filepath.IsAbs(nt) {
			add("policy.never_touch entry %q must be absolute after ~ expansion", nt)
		}
	}
	if p.Quarantine {
		switch {
		case p.QuarantineDir == "":
			add("policy.quarantine is true but quarantine_dir is empty")
		case !filepath.IsAbs(p.QuarantineDir):
			add("policy.quarantine_dir must be absolute after ~ expansion")
		case !p.AllowOutsideHome && !isUnder(p.QuarantineDir, c.home):
			add("policy.quarantine_dir %s must be under %s unless allow_outside_home is true", p.QuarantineDir, c.home)
		}
		if p.QuarantineDays <= 0 {
			add("policy.quarantine_days must be > 0 when quarantine is enabled")
		}
	}

	seen := map[string]bool{}
	all := c.entries(nil)
	check := func(e Entry, kind string) {
		if e.Path == "" {
			add("%s entry with empty path", kind)
			return
		}
		if !filepath.IsAbs(e.Path) {
			add("%s %q must be absolute after ~ expansion", kind, e.Path)
		}
		if seen[e.Path] {
			add("%s %q listed twice", kind, e.Path)
		}
		seen[e.Path] = true
		if e.Type == "" {
			add("%s %q has no type", kind, e.Path)
		}
		if !validActions[e.Action] {
			add("%s %q has invalid action %q (never|rm-contents|rm|rm-stale-children|command)", kind, e.Path, e.Action)
		}
		if e.Action == ActionCommand && strings.TrimSpace(e.Command) == "" {
			add("%s %q has action command but no command", kind, e.Path)
		}
		if e.Action != ActionCommand && e.Command != "" {
			add("%s %q has a command but action is %q", kind, e.Path, e.Action)
		}
		if e.IsFile && (e.Action == ActionRmContents || e.Action == ActionRmStaleChilds) {
			add("known_files %q cannot use %s", e.Path, e.Action)
		}
		if !e.IsFile && e.Action == ActionRm {
			add("known_dirs %q cannot use rm; use rm-contents", e.Path)
		}
		if e.Action == ActionRmStaleChilds && e.StaleAfterHours <= 0 {
			add("%s %q uses rm-stale-children and needs stale_after_hours > 0", kind, e.Path)
		}
		if e.Action != ActionRmStaleChilds && e.StaleAfterHours != 0 {
			add("%s %q sets stale_after_hours but action is %q", kind, e.Path, e.Action)
		}
		if isDestructive(e.Action) {
			for _, f := range []struct{ name, path string }{
				{"log_file", p.LogFile}, {"state_file", p.StateFile}, {"quarantine_dir", p.QuarantineDir},
			} {
				if f.path != "" && (isUnder(f.path, e.Path) || isUnder(e.Path, f.path)) {
					add("%s %q overlaps policy.%s %s; oos must not delete its own records", kind, e.Path, f.name, f.path)
				}
			}
			for _, o := range all {
				if o.Path != e.Path && isDestructive(o.Action) && isUnder(o.Path, e.Path) {
					add("%s %q contains destructive entry %q; nested destructive entries are ambiguous", kind, e.Path, o.Path)
				}
			}
		}
	}
	for _, e := range c.KnownDirs {
		check(e, "known_dirs")
	}
	for _, e := range c.KnownFiles {
		check(e, "known_files")
	}
	if len(errs) > 0 {
		return fmt.Errorf("config invalid:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// entries returns dirs then files, optionally filtered by type.
func (c *Config) entries(types []string) []Entry {
	want := map[string]bool{}
	for _, t := range types {
		t = strings.TrimSpace(strings.ToLower(t))
		if t != "" && t != "all" {
			want[t] = true
		}
	}
	var out []Entry
	for _, e := range append(append([]Entry{}, c.KnownDirs...), c.KnownFiles...) {
		if len(want) == 0 || want[strings.ToLower(e.Type)] {
			out = append(out, e)
		}
	}
	return out
}
