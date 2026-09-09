package size

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// UniqueProbeMin is the size from which a regular file is asked where its
// first block lives. Below it a clone is counted per copy: the probe costs
// an open per file, and small files are where the count is, not the bytes.
var UniqueProbeMin int64 = 128 << 10

type blockKey struct {
	dev  uint64
	id   uint64
	phys bool
}

// Unique walks roots with the same symlink and device rules as PathSizeWalk
// and returns their allocated bytes twice: once as du counts them, and once
// counting blocks that two files share only once. Hardlinks share by inode
// everywhere. On APFS a clone (uv installs, cp -c, Finder duplicates) has
// its own inode but shares every block with its origin, so on darwin regular
// files from UniqueProbeMin up are keyed by the physical position of their
// first block instead. A clone written to since cloning owns its first
// block and counts again. Files under the floor count per copy, so unique
// is an upper bound on what removing the roots returns, never an
// underestimate caused by sharing.
func Unique(roots ...string) (allocated, unique int64, err error) {
	return UniqueAgainst(nil, roots...)
}

// UniqueAgainst is Unique for a removal that leaves keep in place: blocks
// the roots share with anything under keep return nothing, so keep is
// walked first to mark its blocks and is not counted. This is the
// rm-stale-children question, where the stale entries are clones of
// entries that stay.
func UniqueAgainst(keep []string, roots ...string) (allocated, unique int64, err error) {
	seen := map[blockKey]struct{}{}
	for _, k := range keep {
		if _, _, kerr := uniqueWalk(k, seen); kerr != nil && err == nil {
			err = kerr
		}
	}
	for _, root := range roots {
		a, u, werr := uniqueWalk(root, seen)
		allocated += a
		unique += u
		if werr != nil && err == nil {
			err = werr
		}
	}
	return allocated, unique, err
}

func uniqueWalk(root string, seen map[blockKey]struct{}) (allocated, unique int64, err error) {
	info, err := os.Lstat(root)
	if err != nil {
		return 0, 0, err
	}
	count := func(p string, fi fs.FileInfo) {
		b := Allocated(fi)
		allocated += b
		if k, ok := keyOf(p, fi); ok {
			if _, dup := seen[k]; dup {
				return
			}
			seen[k] = struct{}{}
		}
		unique += b
	}
	if !info.IsDir() {
		count(root, info)
		return allocated, unique, nil
	}
	rootDev, haveDev := DeviceOf(info)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) && d != nil && d.IsDir() {
				return fs.SkipDir
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
		count(p, fi)
		return nil
	})
	return allocated, unique, err
}

// keyOf names the blocks behind a regular file. Directories and symlinks
// are never shared and get no key.
func keyOf(p string, fi fs.FileInfo) (blockKey, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.Mode().IsRegular() {
		return blockKey{}, false
	}
	if Allocated(fi) >= UniqueProbeMin {
		if off, ok := physOffset(p); ok {
			return blockKey{dev: uint64(st.Dev), id: off, phys: true}, true
		}
	}
	return blockKey{dev: uint64(st.Dev), id: uint64(st.Ino)}, true
}
