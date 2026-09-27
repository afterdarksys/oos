package safefs

import (
	"os"

	"golang.org/x/sys/unix"
)

// statx reports inode attributes without opening the file.
func immutable(p string, _ os.FileInfo) string {
	var sx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, p, unix.AT_SYMLINK_NOFOLLOW, 0, &sx); err != nil {
		return ""
	}
	switch {
	case sx.Attributes_mask&unix.STATX_ATTR_IMMUTABLE != 0 && sx.Attributes&unix.STATX_ATTR_IMMUTABLE != 0:
		return "immutable"
	case sx.Attributes_mask&unix.STATX_ATTR_APPEND != 0 && sx.Attributes&unix.STATX_ATTR_APPEND != 0:
		return "append-only"
	}
	return ""
}
