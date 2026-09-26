package size

import (
	"context"
	"fmt"
	"github.com/afterdarksys/oos/internal/worklimit"
	"os"
	"path/filepath"
	"syscall"
)

type Accounting struct {
	Logical           int64    `json:"logical_bytes"`
	Allocated         int64    `json:"allocated_bytes"`
	Upper             int64    `json:"reclaimable_upper_bytes"`
	Exclusive         int64    `json:"exclusive_extent_bytes_estimate"`
	Shared            int64    `json:"shared_extent_bytes"`
	ExternalHardlinks int      `json:"externally_linked_files"`
	Unknown           int      `json:"unknown_extent_files"`
	Method            string   `json:"method"`
	Complete          bool     `json:"complete"`
	Notes             []string `json:"notes"`
}
type inodeKey struct{ dev, ino uint64 }
type accountedFile struct {
	path  string
	fi    os.FileInfo
	count uint64
	kept  bool
}
type extentSummary struct {
	exclusive, shared int64
	known             bool
}

// Account uses no cached sizes. Upper is an upper estimate, not a promise:
// retained snapshots/open descriptors and concurrent writers can change recovery.
func Account(ctx context.Context, keep []string, roots ...string) (Accounting, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := Accounting{Method: "inode-links+extent-probes", Complete: true, Notes: []string{"Actual recovery is measured after execution; snapshots and open files may retain blocks."}}
	files := map[inodeKey]*accountedFile{}
	visited := map[string]bool{}
	walk := func(root string, retained bool) error {
		root = filepath.Clean(root)
		info, err := os.Lstat(root)
		if err != nil {
			return err
		}
		dev, _ := DeviceOf(info)
		return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err = worklimit.Step(ctx); err != nil {
				return err
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			if dd, ok := DeviceOf(fi); ok && dd != dev && fi.IsDir() {
				out.Complete = false
				out.Notes = append(out.Notes, "Skipped nested filesystem: "+p)
				return filepath.SkipDir
			}
			if !retained && !visited[p] {
				out.Logical += fi.Size()
				out.Allocated += Allocated(fi)
			}
			if fi.Mode().IsRegular() {
				st, ok := fi.Sys().(*syscall.Stat_t)
				if !ok {
					return fmt.Errorf("inode unavailable for %s", p)
				}
				key := inodeKey{uint64(st.Dev), uint64(st.Ino)}
				f := files[key]
				if f == nil {
					f = &accountedFile{path: p, fi: fi}
					files[key] = f
				}
				if retained {
					f.kept = true
				} else if !visited[p] {
					f.count++
				}
			} else if !retained && !visited[p] {
				out.Upper += Allocated(fi)
			}
			if !retained {
				visited[p] = true
			}
			return nil
		})
	}
	for _, p := range keep {
		if err := walk(p, true); err != nil {
			out.Complete = false
			return out, err
		}
	}
	for _, p := range roots {
		if err := walk(p, false); err != nil {
			out.Complete = false
			return out, err
		}
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			out.Complete = false
			return out, err
		}
		if f.count == 0 {
			continue
		}
		st := f.fi.Sys().(*syscall.Stat_t)
		if f.kept || f.count < uint64(st.Nlink) {
			out.ExternalHardlinks++
			continue
		}
		out.Upper += Allocated(f.fi)
		ex, err := probeExtents(ctx, f.path, f.fi)
		if err != nil || !ex.known {
			out.Unknown++
			continue
		}
		out.Exclusive += ex.exclusive
		out.Shared += ex.shared
	}
	if out.Unknown > 0 {
		out.Notes = append(out.Notes, "Some extent ownership is unknown; allocated upper estimates are retained.")
	}
	return out, nil
}
