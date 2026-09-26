//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

// checkOwner refuses a config not owned by the effective user, or one the
// group or others may write.
func checkOwner(path string, fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("config %s: cannot read its owner", path)
	}
	if euid := os.Geteuid(); int(st.Uid) != euid {
		return fmt.Errorf("config %s is owned by uid %d, not %d; refusing to load it", path, st.Uid, euid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("config %s is writable by group or others (mode %04o); refusing to load it (chmod go-w)", path, fi.Mode().Perm())
	}
	return nil
}
