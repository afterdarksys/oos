package size

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// Linux UAPI layout: struct fiemap (32 bytes) followed by 56-byte extents.
type fiemapExtent struct {
	Logical, Physical, Length uint64
	Reserved                  [2]uint64
	Flags                     uint32
	Reserved32                [3]uint32
}
type fiemapBuffer struct {
	Start, Length                  uint64
	Flags, Mapped, Count, Reserved uint32
	Extents                        [128]fiemapExtent
}

func probeExtents(ctx context.Context, path string, expected os.FileInfo) (extentSummary, error) {
	var result extentSummary
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return result, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !os.SameFile(before, expected) || before.Size() != expected.Size() || !before.ModTime().Equal(expected.ModTime()) {
		return result, fmt.Errorf("file changed before extent probe")
	}
	var start uint64
	for pages := 0; pages < 8192; pages++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		b := fiemapBuffer{Start: start, Length: ^uint64(0) - start, Count: 128}
		// No FIEMAP_FLAG_SYNC: reporting must not force writeback.
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), 0xc020660b, uintptr(unsafe.Pointer(&b)))
		runtime.KeepAlive(&b)
		if errno != 0 {
			return result, errno
		}
		if b.Mapped > 128 {
			return result, fmt.Errorf("invalid FIEMAP extent count")
		}
		if b.Mapped == 0 {
			result.known = true
			break
		}
		last := false
		for _, e := range b.Extents[:b.Mapped] {
			// Unknown, delayed, encoded/encrypted, inline, unaligned and future flags
			// are not suitable for exclusive-byte estimation.
			const supported = uint32(0x1 | 0x800 | 0x1000 | 0x2000) // LAST, UNWRITTEN, MERGED, SHARED
			if e.Flags & ^supported != 0 {
				return extentSummary{}, nil
			}
			if e.Length > uint64(^uint64(0)>>1) || e.Logical+e.Length < e.Logical {
				return extentSummary{}, fmt.Errorf("invalid FIEMAP extent")
			}
			if e.Flags&0x2000 != 0 {
				result.shared += int64(e.Length)
			} else {
				result.exclusive += int64(e.Length)
			}
			next := e.Logical + e.Length
			if next <= start {
				return extentSummary{}, fmt.Errorf("non-progressing FIEMAP")
			}
			start = next
			last = e.Flags&1 != 0
		}
		if last {
			result.known = true
			break
		}
	}
	after, err := f.Stat()
	if err != nil || !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		return extentSummary{}, fmt.Errorf("file changed during extent probe")
	}
	if result.exclusive+result.shared > Allocated(after) {
		return extentSummary{}, nil
	}
	return result, nil
}
