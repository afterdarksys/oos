package config

import (
	"path/filepath"
	"strings"
)

// IsUnder reports whether p equals base or lives inside it. A base of "/"
// matches only "/" itself: listing the root in never_touch protects the root,
// it does not protect every path on the machine (the home and depth rules do
// that job with intent).
func IsUnder(p, base string) bool {
	p = filepath.Clean(p)
	base = filepath.Clean(base)
	if p == base {
		return true
	}
	if base == string(filepath.Separator) {
		return false
	}
	return strings.HasPrefix(p, base+string(filepath.Separator))
}

func PathDepth(p string) int {
	p = filepath.Clean(p)
	if p == string(filepath.Separator) {
		return 0
	}
	return strings.Count(p, string(filepath.Separator))
}
