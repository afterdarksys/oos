package guard

import (
	"path/filepath"
	"strings"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/protect"
	"github.com/afterdarksys/oos/internal/safefs"
)

// CheckRemovalPath rejects both protected paths and ancestors containing them.
// It is shared by planning and execution, including individual child removals.
func CheckRemovalPath(p config.Policy, path, home string) error {
	if err := safefs.CheckAncestors(path); err != nil {
		return Refuse("symlink", "%v", err)
	}
	path = safefs.CanonicalAlias(path)
	home = safefs.CanonicalAlias(home)
	if prefix, ok := protect.Hit(path, home, p.AlwaysDisallowed); ok {
		return Refuse("always_disallowed", "%s is protected by %s", path, prefix)
	}
	for _, raw := range append(append([]string{}, protect.Builtin...), p.AlwaysDisallowed...) {
		if strings.HasPrefix(raw, "~/") {
			if home == "." || home == "" {
				continue
			}
			raw = filepath.Join(home, strings.TrimPrefix(raw, "~/"))
		}
		base := safefs.CanonicalAlias(raw)
		if base != "/" && config.IsUnder(base, path) {
			return Refuse("always_disallowed", "%s contains protected %s", path, base)
		}
	}
	for _, raw := range p.NeverTouch {
		base := safefs.CanonicalAlias(raw)
		if config.IsUnder(path, base) || (base != "/" && config.IsUnder(base, path)) {
			return Refuse("never_touch", "%s overlaps protected %s", path, base)
		}
	}
	return nil
}
