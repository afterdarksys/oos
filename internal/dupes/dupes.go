// Package dupes finds files with identical content under a root. Size
// first, then a cheap head-and-tail hash, then a full SHA-256 only for the
// files that still match, so a 50 GB Downloads folder costs one read of the
// candidates and nothing else. Hardlinks are one file, never a pair.
// Read-only: the report says what is repeated and which copy is newest.
package dupes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/afterdarksys/oos/internal/size"
)

const (
	DefaultMinBytes = 10 << 20 // below this a duplicate is not worth a line
	quickBytes      = 64 << 10 // head and tail sampled before the full hash
)

// File is one copy.
type File struct {
	Path    string    `json:"path"`
	Bytes   int64     `json:"bytes"`
	ModTime time.Time `json:"mtime"`
	inode   uint64
	dev     uint64
}

// Group is one set of identical files.
type Group struct {
	Bytes  int64  `json:"bytes"`  // size of one copy
	Wasted int64  `json:"wasted"` // bytes the extra copies hold
	Hash   string `json:"sha256"`
	Files  []File `json:"files"` // newest first
}

// Result is the whole report.
type Result struct {
	Root    string  `json:"root"`
	Scanned int     `json:"files_scanned"`
	Groups  []Group `json:"groups"`
	Wasted  int64   `json:"wasted_bytes"`
	Elapsed time.Duration
}

func ids(fi fs.FileInfo) (dev, ino uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), uint64(st.Ino)
	}
	return 0, 0
}

// Find walks root for regular files of at least minBytes that pass the
// filter, and returns the groups of identical content, most wasted first.
func Find(root string, minBytes int64, f size.Filter, now time.Time) (*Result, error) {
	start := time.Now()
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	rootDev, haveDev := size.DeviceOf(info)
	if minBytes <= 0 {
		minBytes = DefaultMinBytes
	}
	res := &Result{Root: root}
	bySize := map[int64][]File{}
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
				if dev, ok := size.DeviceOf(fi); ok && dev != rootDev {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !fi.Mode().IsRegular() || fi.Size() < minBytes || !f.KeepTime(fi.ModTime(), now) || !f.KeepName(p) {
			return nil
		}
		res.Scanned++
		dev, ino := ids(fi)
		bySize[fi.Size()] = append(bySize[fi.Size()], File{Path: p, Bytes: fi.Size(), ModTime: fi.ModTime(), inode: ino, dev: dev})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for sz, files := range bySize {
		if len(files) < 2 {
			continue
		}
		files = dropHardlinks(files)
		if len(files) < 2 {
			continue
		}
		byQuick := map[string][]File{}
		for _, fl := range files {
			h, err := quickHash(fl.Path, sz)
			if err != nil {
				continue
			}
			byQuick[h] = append(byQuick[h], fl)
		}
		for _, cand := range byQuick {
			if len(cand) < 2 {
				continue
			}
			byFull := map[string][]File{}
			for _, fl := range cand {
				h, err := fullHash(fl.Path)
				if err != nil {
					continue
				}
				byFull[h] = append(byFull[h], fl)
			}
			for h, same := range byFull {
				if len(same) < 2 {
					continue
				}
				sort.Slice(same, func(i, j int) bool { return same[i].ModTime.After(same[j].ModTime) })
				g := Group{Bytes: sz, Wasted: sz * int64(len(same)-1), Hash: h, Files: same}
				res.Groups = append(res.Groups, g)
				res.Wasted += g.Wasted
			}
		}
	}
	sort.Slice(res.Groups, func(i, j int) bool {
		if res.Groups[i].Wasted != res.Groups[j].Wasted {
			return res.Groups[i].Wasted > res.Groups[j].Wasted
		}
		return res.Groups[i].Files[0].Path < res.Groups[j].Files[0].Path
	})
	res.Elapsed = time.Since(start)
	return res, nil
}

// dropHardlinks keeps one path per (device, inode): the same bytes on disk
// are not a duplicate, and deleting one link frees nothing.
func dropHardlinks(files []File) []File {
	seen := map[[2]uint64]bool{}
	var out []File
	for _, f := range files {
		k := [2]uint64{f.dev, f.inode}
		if f.inode != 0 && seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

func quickHash(p string, sz int64) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	head := make([]byte, quickBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	h.Write(head[:n])
	if sz > 2*quickBytes {
		if _, err := f.Seek(sz-quickBytes, io.SeekStart); err != nil {
			return "", err
		}
		tail := make([]byte, quickBytes)
		n, err := io.ReadFull(f, tail)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return "", err
		}
		h.Write(tail[:n])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fullHash(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
