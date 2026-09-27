//go:build linux

package size

import "syscall"

// blockSize is the unit f_blocks and f_bavail count in. Linux counts them
// in fragments (f_frsize); f_bsize is only the preferred I/O size, which
// differs on some filesystems (XFS with a large stripe unit, NFS, FUSE).
// An old kernel or filesystem that leaves f_frsize zero falls back to f_bsize.
func blockSize(st *syscall.Statfs_t) uint64 {
	if st.Frsize > 0 {
		return uint64(st.Frsize)
	}
	return uint64(st.Bsize)
}
