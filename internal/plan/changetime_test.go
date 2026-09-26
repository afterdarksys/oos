package plan

import "github.com/afterdarksys/oos/internal/guard"

// Fixtures are aged with Chtimes, which cannot move ctime back. Tests that
// need ctime turn it on themselves.
func init() { guard.StaleUsesChangeTime = false }
