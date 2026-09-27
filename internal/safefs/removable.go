package safefs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// CheckRemovable walks path without following symlinks and reports the first
// object this process could not remove: an immutable or append-only flag
// (chflags uchg/schg, chattr +i/+a), or a directory whose entries it may not
// unlink (owned by someone else and not writable, e.g. root-owned leftovers
// of `sudo npm`). Removal chmods directories this process owns, so an owned
// read-only directory is fine. It is a preflight: a race can still fail the
// removal, which callers must handle anyway.
func CheckRemovable(ctx context.Context, path string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	euid := os.Geteuid()
	return filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if why := immutable(p, fi); why != "" {
			return fmt.Errorf("%s is %s; clear it (chflags nouchg / chattr -i -a) before purging", p, why)
		}
		if p != path {
			parent := filepath.Dir(p)
			if pfi, err := os.Lstat(parent); err == nil && pfi.Mode()&os.ModeSticky != 0 && euid != 0 {
				if owner(fi) != euid && owner(pfi) != euid {
					return fmt.Errorf("%s is in a sticky directory and owned by uid %d; cannot unlink as uid %d", p, owner(fi), euid)
				}
			}
		}
		if !d.IsDir() || euid == 0 || owner(fi) == euid {
			return nil
		}
		if err := unix.Faccessat(unix.AT_FDCWD, p, unix.W_OK|unix.X_OK, unix.AT_EACCESS); err != nil {
			return fmt.Errorf("%s is owned by uid %d and not writable by uid %d; remove it as that user (e.g. sudo rm -rf)", p, owner(fi), euid)
		}
		return nil
	})
}

func owner(fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}
