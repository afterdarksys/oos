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

// DiskUsage is the volume's free and total bytes.
type DiskUsage struct {
	Free  uint64
	Total uint64
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
	bs := uint64(st.Bsize)
	return DiskUsage{Free: st.Bavail * bs, Total: st.Blocks * bs}, nil
}

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

// BigFile is one scan hit.
type BigFile struct {
	Path    string    `json:"path"`
	Bytes   int64     `json:"bytes"`
	ModTime time.Time `json:"mtime"`
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
