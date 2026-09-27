//go:build !linux

package plan

import "golang.org/x/sys/unix"

// samePool compares statfs f_fsid and filesystem type when st_dev differs.
func samePool(a, b string) bool {
	var sa, sb unix.Statfs_t
	if unix.Statfs(a, &sa) != nil || unix.Statfs(b, &sb) != nil {
		return false
	}
	return sa.Type == sb.Type && sa.Fstypename == sb.Fstypename && sa.Fsid == sb.Fsid && sa.Fsid != (unix.Fsid{})
}
