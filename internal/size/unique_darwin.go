package size

import (
	"syscall"
	"unsafe"
)

// fLog2Phys is F_LOG2PHYS from <sys/fcntl.h>: map the file's first logical
// block to its position on the device.
const fLog2Phys = 49

// log2phys mirrors struct log2phys, which the header wraps in #pragma
// pack(4): 20 bytes, l2p_flags at 0, l2p_contigbytes at 4, l2p_devoffset
// at 12. Go would align the two off_t fields to 8 and read the offset's
// upper half at 16, so the 64-bit fields are held as 32-bit halves.
type log2phys struct {
	flags       uint32
	contigLo    uint32
	contigHi    uint32
	devOffsetLo uint32
	devOffsetHi uint32
}

// physOffset returns where the file's first block sits on the device. Two
// files answering the same offset share that block: hardlinks, or APFS
// clones that have not diverged. An unreadable file (TCC-protected folders
// under a launchd agent) answers false and is keyed by inode instead.
func physOffset(p string) (uint64, bool) {
	fd, err := syscall.Open(p, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return 0, false
	}
	defer syscall.Close(fd)
	var l log2phys
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), fLog2Phys, uintptr(unsafe.Pointer(&l))); errno != 0 {
		return 0, false
	}
	return uint64(l.devOffsetLo) | uint64(l.devOffsetHi)<<32, true
}
