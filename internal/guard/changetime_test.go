package guard

// Fixtures are aged with Chtimes, which cannot move ctime back. Tests that
// need ctime turn it on themselves.
func init() { StaleUsesChangeTime = false }
