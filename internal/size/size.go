package size

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

const GB = 1024 * 1024 * 1024

// DiskUsage is the volume's free and total bytes, plus inodes when the
// filesystem reports them. An app's "no space left on device" with df
// showing free bytes is often an empty inode table (df -i), a different
// volume than the one df was pointed at, or space df counts that the app
// is not allowed to use.
type DiskUsage struct {
	Free        uint64
	Total       uint64
	InodesFree  uint64
	InodesTotal uint64
}

func (d DiskUsage) FreeGB() float64  { return float64(d.Free) / GB }
func (d DiskUsage) TotalGB() float64 { return float64(d.Total) / GB }
func (d DiskUsage) FreePct() float64 {
	if d.Total == 0 {
		return 0
	}
	return 100 * float64(d.Free) / float64(d.Total)
}

func Disk(path string) (DiskUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return DiskUsage{}, err
	}
	bs := blockSize(&st)
	return DiskUsage{Free: st.Bavail * bs, Total: st.Blocks * bs, InodesFree: st.Ffree, InodesTotal: st.Files}, nil
}

// InodesScarce reports when creating a file can fail with ENOSPC while
// byte-free still looks comfortable. APFS reports a huge inode pool, so
// this stays quiet there. ext4 does not.
func (d DiskUsage) InodesScarce() bool {
	if d.InodesTotal == 0 {
		return false
	}
	return d.InodesFree < 1000 || d.InodesFree*100 < d.InodesTotal
}

// Sync commits filesystem transactions so a following statfs matches what
// df will print. It does not free blocks held by a snapshot, by a process
// that still has a deleted file open, or by a rename into quarantine.
func Sync() { syscall.Sync() }

// Allocated returns the on-disk bytes for a file (blocks*512), falling back
// to the apparent size. Sparse files like Docker.raw report far less than Size.
func Allocated(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return info.Size()
}

func DeviceOf(info fs.FileInfo) (uint64, bool) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), true
	}
	return 0, false
}

// PathSizeWalk returns allocated bytes under root without the cache. It never
// follows symlinks and never crosses onto another device, so a mounted volume
// or a link out of the tree is not counted as part of it. Permission errors on
// subtrees are skipped, not fatal, because caches routinely contain unreadable
// corners.
func PathSizeWalk(root string) (int64, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return Allocated(info), nil
	}
	rootDev, haveDev := DeviceOf(info)
	var total int64
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			return nil
		}
		fi, err := d.Info() // Lstat semantics; symlinks are counted as links
		if err != nil {
			return nil
		}
		if d.IsDir() && haveDev {
			if dev, ok := DeviceOf(fi); ok && dev != rootDev {
				return fs.SkipDir
			}
		}
		total += Allocated(fi)
		return nil
	})
	return total, err
}

// BigFile is one scan hit. Taken and Device come from picture metadata
// when the file has it. They are labels; nothing decides a deletion from them.
type BigFile struct {
	Path    string    `json:"path"`
	Bytes   int64     `json:"bytes"`
	ModTime time.Time `json:"mtime"`
	Taken   string    `json:"taken,omitempty"`
	Device  string    `json:"device,omitempty"`
}

// ScanBig lists regular files under root with at least minBytes allocated,
// largest first, capped at topN. Same symlink and device rules as pathSize.
func ScanBig(root string, minBytes int64, topN int) ([]BigFile, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("scan root must be a directory")
	}
	rootDev, haveDev := DeviceOf(info)
	var hits []BigFile
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if haveDev {
				if dev, ok := DeviceOf(fi); ok && dev != rootDev {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		if b := Allocated(fi); b >= minBytes {
			hits = append(hits, BigFile{Path: p, Bytes: b, ModTime: fi.ModTime()})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Bytes > hits[j].Bytes })
	if topN > 0 && len(hits) > topN {
		hits = hits[:topN]
	}
	return hits, nil
}
