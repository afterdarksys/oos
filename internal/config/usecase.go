package config

// Use-case attribution: what the bytes are for, not just where they sit.
// Three sources in priority order: the entry's own use_case, an owners
// pattern from the policy, and automatic attribution from fingerprints on
// disk (a Cargo.toml beside a target dir, a package.json beside
// node_modules, a pyvenv.cfg, a .git).

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// OwnerFor returns the first owner whose pattern covers p. A plain path
// matches itself and everything under it; a glob is tried against p and
// each of its ancestors, so "~/development/*/target" also covers
// "~/development/foo/target/debug".
func (c *Config) OwnerFor(p string) (Owner, bool) {
	p = filepath.Clean(p)
	for _, o := range c.Policy.Owners {
		if !hasMeta(o.Match) {
			if IsUnder(p, o.Match) {
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

// UseCaseFor answers "what is this for" and where the answer came from:
// entry, owner, auto, or "" when nothing knows.
func (c *Config) UseCaseFor(p string) (label, source string) {
	p = filepath.Clean(p)
	for _, e := range c.Entries(nil) {
		if e.UseCase != "" && (e.Path == p || IsUnder(p, e.Path)) {
			return e.UseCase, "entry"
		}
	}
	if o, ok := c.OwnerFor(p); ok {
		return o.UseCase, "owner"
	}
	if a := Attribute(p); a.Label != "" {
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

func Exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// GitRoot returns the nearest ancestor (or p itself) containing .git.
func GitRoot(p string) string {
	for d := filepath.Clean(p); ; d = filepath.Dir(d) {
		if Exists(filepath.Join(d, ".git")) {
			return d
		}
		if d == filepath.Dir(d) {
			return ""
		}
	}
}

func Attribute(p string) Attribution {
	p = filepath.Clean(p)
	a := Attribution{}
	name := filepath.Base(p)
	parent := filepath.Dir(p)
	if root := GitRoot(p); root != "" && root != p {
		a.Repo = filepath.Base(root)
	}
	switch {
	case name == "target" && Exists(filepath.Join(parent, "Cargo.toml")):
		a.Label, a.Fingerprint = "Rust build output", "Cargo.toml beside target"
	case name == "node_modules" && Exists(filepath.Join(parent, "package.json")):
		a.Label, a.Fingerprint = "npm dependencies", "package.json beside node_modules"
	case Exists(filepath.Join(p, "pyvenv.cfg")):
		a.Label, a.Fingerprint = "Python virtualenv", "pyvenv.cfg"
	case name == "DerivedData":
		a.Label, a.Fingerprint = "Xcode build output", "DerivedData"
	case name == ".terraform":
		a.Label, a.Fingerprint = "Terraform providers", ".terraform"
	case name == ".next" && Exists(filepath.Join(parent, "package.json")):
		a.Label, a.Fingerprint = "Next.js build output", ".next beside package.json"
	case (name == "dist" || name == "build") && Exists(filepath.Join(parent, "package.json")):
		a.Label, a.Fingerprint = "JS build output", name+" beside package.json"
	case Exists(filepath.Join(p, ".git")):
		a.Label, a.Fingerprint = "git repository", ".git"
	case Exists(filepath.Join(p, "Cargo.toml")):
		a.Label, a.Fingerprint = "Rust project", "Cargo.toml"
	case Exists(filepath.Join(p, "go.mod")):
		a.Label, a.Fingerprint = "Go module", "go.mod"
	case Exists(filepath.Join(p, "package.json")):
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
