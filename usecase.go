package main

// Use-case attribution: what the bytes are for, not just where they sit.
// Three sources in priority order: the entry's own use_case, an owners
// pattern from the policy, and automatic attribution from fingerprints on
// disk (a Cargo.toml beside a target dir, a package.json beside
// node_modules, a pyvenv.cfg, a .git).

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Owner maps a path pattern to a use case label.
type Owner struct {
	Match   string `json:"match"`
	UseCase string `json:"use_case"`
	Note    string `json:"note,omitempty"`
}

func hasMeta(p string) bool { return strings.ContainsAny(p, "*?[") }

// globRegexp turns an owner pattern into a regexp: "**" spans directories,
// "*" and "?" stay inside one path component, anything else is literal.
func globRegexp(pat string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pat); i++ {
		switch {
		case strings.HasPrefix(pat[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(pat[i:], "**"):
			b.WriteString(".*")
			i++
		case pat[i] == '*':
			b.WriteString("[^/]*")
		case pat[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(pat[i : i+1]))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// ownerFor returns the first owner whose pattern covers p. A plain path
// matches itself and everything under it; a glob is tried against p and
// each of its ancestors, so "~/development/*/target" also covers
// "~/development/foo/target/debug".
func (c *Config) ownerFor(p string) (Owner, bool) {
	p = filepath.Clean(p)
	for _, o := range c.Policy.Owners {
		if !hasMeta(o.Match) {
			if isUnder(p, o.Match) {
				return o, true
			}
			continue
		}
		re, err := globRegexp(o.Match)
		if err != nil {
			continue
		}
		for cand := p; ; cand = filepath.Dir(cand) {
			if re.MatchString(cand) {
				return o, true
			}
			if cand == filepath.Dir(cand) {
				break
			}
		}
	}
	return Owner{}, false
}

// useCaseFor answers "what is this for" and where the answer came from:
// entry, owner, auto, or "" when nothing knows.
func (c *Config) useCaseFor(p string) (label, source string) {
	p = filepath.Clean(p)
	for _, e := range c.entries(nil) {
		if e.UseCase != "" && (e.Path == p || isUnder(p, e.Path)) {
			return e.UseCase, "entry"
		}
	}
	if o, ok := c.ownerFor(p); ok {
		return o.UseCase, "owner"
	}
	if a := attribute(p); a.Label != "" {
		return a.Label, "auto"
	}
	return "", ""
}

// Attribution is what the filesystem itself says about a path.
type Attribution struct {
	Label       string `json:"label,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// gitRoot returns the nearest ancestor (or p itself) containing .git.
func gitRoot(p string) string {
	for d := filepath.Clean(p); ; d = filepath.Dir(d) {
		if exists(filepath.Join(d, ".git")) {
			return d
		}
		if d == filepath.Dir(d) {
			return ""
		}
	}
}

func attribute(p string) Attribution {
	p = filepath.Clean(p)
	a := Attribution{}
	name := filepath.Base(p)
	parent := filepath.Dir(p)
	if root := gitRoot(p); root != "" && root != p {
		a.Repo = filepath.Base(root)
	}
	switch {
	case name == "target" && exists(filepath.Join(parent, "Cargo.toml")):
		a.Label, a.Fingerprint = "Rust build output", "Cargo.toml beside target"
	case name == "node_modules" && exists(filepath.Join(parent, "package.json")):
		a.Label, a.Fingerprint = "npm dependencies", "package.json beside node_modules"
	case exists(filepath.Join(p, "pyvenv.cfg")):
		a.Label, a.Fingerprint = "Python virtualenv", "pyvenv.cfg"
	case name == "DerivedData":
		a.Label, a.Fingerprint = "Xcode build output", "DerivedData"
	case name == ".terraform":
		a.Label, a.Fingerprint = "Terraform providers", ".terraform"
	case name == ".next" && exists(filepath.Join(parent, "package.json")):
		a.Label, a.Fingerprint = "Next.js build output", ".next beside package.json"
	case (name == "dist" || name == "build") && exists(filepath.Join(parent, "package.json")):
		a.Label, a.Fingerprint = "JS build output", name+" beside package.json"
	case exists(filepath.Join(p, ".git")):
		a.Label, a.Fingerprint = "git repository", ".git"
	case exists(filepath.Join(p, "Cargo.toml")):
		a.Label, a.Fingerprint = "Rust project", "Cargo.toml"
	case exists(filepath.Join(p, "go.mod")):
		a.Label, a.Fingerprint = "Go module", "go.mod"
	case exists(filepath.Join(p, "package.json")):
		a.Label, a.Fingerprint = "Node project", "package.json"
	}
	if a.Repo != "" {
		if a.Label != "" {
			a.Label += " (" + a.Repo + ")"
		} else {
			a.Label = "inside repo " + a.Repo
		}
	}
	return a
}

// newestFile finds the most recently modified regular file under p, with a
// bounded walk so a huge tree answers in seconds rather than minutes.
func newestFile(p string, maxEntries int) (string, time.Time) {
	var best string
	var bestT time.Time
	n := 0
	_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		n++
		if n > maxEntries {
			return filepath.SkipAll
		}
		if d.IsDir() && strings.Count(strings.TrimPrefix(q, p), string(filepath.Separator)) >= 4 {
			return filepath.SkipDir
		}
		if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() && fi.ModTime().After(bestT) {
			best, bestT = q, fi.ModTime()
		}
		return nil
	})
	return best, bestT
}

// processHead trims a command line to something a human can scan.
func processHead(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if len(cmd) > 100 {
		cmd = cmd[:100] + "..."
	}
	return cmd
}

// whoResult is the answer to "what is this path for and who is using it".
type whoResult struct {
	Path        string      `json:"path"`
	Bytes       int64       `json:"bytes"`
	UseCase     string      `json:"use_case,omitempty"`
	Source      string      `json:"use_case_source,omitempty"`
	Attribution Attribution `json:"attribution"`
	Processes   []string    `json:"processes"`
	ProcessN    int         `json:"process_count"`
	NewestFile  string      `json:"newest_file,omitempty"`
	NewestAt    time.Time   `json:"newest_at,omitempty"`
}

func doWho(cfg *Config, env Env, o *opts, now time.Time, out, errw io.Writer) int {
	p := expandHome(o.who, env.Home)
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	if !exists(p) {
		fmt.Fprintf(errw, "oos: %s does not exist\n", p)
		return exitUsage
	}
	r := whoResult{Path: p, Attribution: attribute(p)}
	r.UseCase, r.Source = cfg.useCaseFor(p)
	if !o.quick {
		r.Bytes, _ = pathSize(p)
	}
	if refs, err := env.references(); err == nil {
		seen := map[string]bool{}
		for _, ref := range refs {
			if referenced(p, []string{ref}) {
				r.ProcessN++
				h := processHead(ref)
				if !seen[h] && len(r.Processes) < 5 {
					seen[h] = true
					r.Processes = append(r.Processes, h)
				}
			}
		}
	}
	r.NewestFile, r.NewestAt = newestFile(p, 50000)
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(r)
		return exitOK
	}
	fmt.Fprintln(out, p)
	if !o.quick {
		fmt.Fprintf(out, "  size        %s\n", human(r.Bytes))
	}
	if r.UseCase != "" {
		fmt.Fprintf(out, "  use case    %s  (from %s)\n", r.UseCase, r.Source)
	} else {
		fmt.Fprintln(out, "  use case    unknown; add an owners pattern or --add it with --use-case")
	}
	if r.Attribution.Repo != "" {
		fmt.Fprintf(out, "  repo        %s\n", r.Attribution.Repo)
	}
	if r.Attribution.Fingerprint != "" {
		fmt.Fprintf(out, "  fingerprint %s\n", r.Attribution.Fingerprint)
	}
	if r.ProcessN > 0 {
		fmt.Fprintf(out, "  processes   %d referencing it\n", r.ProcessN)
		for _, h := range r.Processes {
			fmt.Fprintf(out, "              %s\n", h)
		}
	} else {
		fmt.Fprintln(out, "  processes   none reference it")
	}
	if r.NewestFile != "" {
		fmt.Fprintf(out, "  newest      %s  (%s ago)\n", r.NewestFile, now.Sub(r.NewestAt).Round(time.Minute))
	}
	return exitOK
}

// useCaseTotal is one row of a by-use-case summary.
type useCaseTotal struct {
	UseCase string `json:"use_case"`
	Bytes   int64  `json:"bytes"`
	Count   int    `json:"count"`
}

// groupByUseCase sums bytes per use case. Paths nobody can attribute land
// under "unattributed" so the gap is visible rather than hidden.
func groupByUseCase(cfg *Config, paths []string, bytes []int64) []useCaseTotal {
	acc := map[string]*useCaseTotal{}
	for i, p := range paths {
		label, _ := cfg.useCaseFor(p)
		if label == "" {
			label = "unattributed"
		}
		t, ok := acc[label]
		if !ok {
			t = &useCaseTotal{UseCase: label}
			acc[label] = t
		}
		t.Bytes += bytes[i]
		t.Count++
	}
	out := make([]useCaseTotal, 0, len(acc))
	for _, t := range acc {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

func printUseCaseTotals(out io.Writer, totals []useCaseTotal) {
	if len(totals) == 0 {
		return
	}
	fmt.Fprintln(out, "by use case:")
	for _, t := range totals {
		fmt.Fprintf(out, "  %9s  %3d  %s\n", human(t.Bytes), t.Count, t.UseCase)
	}
}
