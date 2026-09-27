package guard

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/protect"
	"github.com/afterdarksys/oos/internal/safefs"
)

// CheckRemovalPath rejects both protected paths and ancestors containing them.
// It is shared by planning and execution, including individual child removals.
func CheckRemovalPath(p config.Policy, path, home string) error {
	return checkPath(p, path, home, true)
}

// CheckCommandPath is the path rule for a command entry. A command does not
// remove the directory it is filed under, so a built-in or always_disallowed
// path below it is not a reason to refuse (/var/lib/docker anchors docker
// builder prune although it holds the protected volumes); what the command
// touches is the text scan's business (protect.CommandHits). The path itself
// being protected is refused, and never_touch and oos's own data are judged
// both ways exactly as for a removal.
func CheckCommandPath(p config.Policy, path, home string) error {
	return checkPath(p, path, home, false)
}

// checkPath judges path against the protected, never_touch and own-data
// lists. contains also refuses a path with a protected path below it, which
// a removal of path would take with it.
func checkPath(p config.Policy, path, home string, contains bool) error {
	// The lexical check first: a protected path is refused as protected
	// without touching the filesystem, even where its parent is unreadable.
	if prefix, ok := protect.Hit(filepath.Clean(path), home, p.AlwaysDisallowed); ok {
		return Refuse("always_disallowed", "%s is protected by %s", path, prefix)
	}
	if err := safefs.CheckAncestors(path); err != nil {
		return Refuse("symlink", "%v", err)
	}
	path = safefs.CanonicalAlias(path)
	home = safefs.CanonicalAlias(home)
	if prefix, ok := protect.Hit(path, home, p.AlwaysDisallowed); ok {
		return Refuse("always_disallowed", "%s is protected by %s", path, prefix)
	}
	for i, raw := range append(append([]string{}, protect.Builtin...), p.AlwaysDisallowed...) {
		// Built-in cache anchors (/root/.cache, /var/lib/docker, ...) are carved
		// out of their protected parent; a config-added entry is never lifted.
		if i < len(protect.Builtin) && protect.Excepted(path, raw) {
			continue
		}
		if strings.HasPrefix(raw, "~/") {
			if home == "." || home == "" {
				continue
			}
			raw = filepath.Join(home, strings.TrimPrefix(raw, "~/"))
		}
		base := safefs.CanonicalAlias(raw)
		// Preserve the explicit /usr/local exception, including filesystem aliases.
		if base == "/usr" && physicalUnder(path, "/usr/local") {
			continue
		}
		if base == "/" {
			continue
		}
		if physicalUnder(path, base) {
			return Refuse("always_disallowed", "%s is protected by %s", path, base)
		}
		if contains && (config.IsUnder(base, path) || physicalUnder(base, path)) {
			return Refuse("always_disallowed", "%s contains protected %s", path, base)
		}
	}
	for _, raw := range p.NeverTouch {
		base := safefs.CanonicalAlias(raw)
		if config.IsUnder(path, base) || (base != "/" && (config.IsUnder(base, path) || physicalUnder(path, base) || physicalUnder(base, path))) {
			return Refuse("never_touch", "%s overlaps protected %s", path, base)
		}
	}
	for _, own := range OwnData(p, home) {
		base := safefs.CanonicalAlias(own)
		if config.IsUnder(path, base) || config.IsUnder(base, path) || physicalUnder(path, base) || physicalUnder(base, path) {
			return Refuse("own_data", "%s overlaps oos's own %s", path, base)
		}
	}
	return nil
}

// OwnData is every path oos keeps its records in: quarantine stores, the
// state file and its companions, the log and the mutation lock. The config
// loader checks entries against these lexically; CheckRemovalPath checks
// every removal at run time, through case and inode aliases.
func OwnData(p config.Policy, home string) []string {
	out := p.QuarantineStores()
	for _, f := range []string{p.LogFile, p.StateFile, p.SizeCacheFile} {
		if f != "" {
			out = append(out, f)
		}
	}
	if p.StateFile != "" {
		out = append(out, p.StateFile+".quarantine-index.json", p.StateFile+".auto-act.json")
	}
	if home != "" && home != "." && filepath.IsAbs(home) {
		out = append(out, filepath.Join(home, ".local", "state", "oos", "mutation.lock"))
	}
	return out
}

// physicalUnder compares existing object identities along the path's ancestry.
// This recognizes case and Unicode aliases without guessing filesystem rules.
func physicalUnder(path, base string) bool {
	b, err := os.Stat(base)
	if err != nil {
		return false
	}
	for {
		f, err := os.Stat(path)
		if err == nil && os.SameFile(f, b) {
			return true
		}
		next := filepath.Dir(path)
		if next == path {
			return false
		}
		path = next
	}
}
