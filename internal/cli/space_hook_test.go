package cli

import "github.com/afterdarksys/oos/internal/space"

func init() {
	// A sized --check in a test must not walk this machine's temporary
	// directory or ask CacheDelete.
	space.Enabled = false
}
