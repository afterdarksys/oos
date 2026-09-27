//go:build darwin

package size

import "syscall"

// blockSize is the unit f_blocks and f_bavail count in: f_bsize on macOS,
// which has no f_frsize.
func blockSize(st *syscall.Statfs_t) uint64 {
	return uint64(st.Bsize)
}
