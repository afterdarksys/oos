// Package safefs implements strict, uncached walks and bounded removal.
package safefs

import (
	"context"
	"fmt"
	"github.com/afterdarksys/oos/internal/worklimit"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/afterdarksys/oos/internal/size"
)

// CanonicalAlias expands only macOS's standard root-level aliases. Arbitrary
// user-created symlink ancestors are never accepted by OpenDir.
func CanonicalAlias(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "darwin" {
		for _, alias := range []string{"/var", "/tmp", "/etc"} {
			if path == alias || strings.HasPrefix(path, alias+"/") {
				if target, err := os.Readlink(alias); err == nil && (target == "private"+alias || target == "/private"+alias) {
					return "/private" + path
				}
			}
		}
	}
	return path
}

// OpenDir pins each ancestor before opening the next. Lstat plus SameFile
// rejects a substituted symlink/directory; all further operations use the handle.
func OpenDir(path string) (*os.Root, error) {
	path = CanonicalAlias(path)
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("not absolute: %s", path)
	}
	r, err := os.OpenRoot("/")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, err := openChild(r, part)
		r.Close()
		if err != nil {
			return nil, err
		}
		r = next
	}
	return r, nil
}

func openChild(r *os.Root, name string) (*os.Root, error) {
	before, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("not a real directory: %s/%s", r.Name(), name)
	}
	child, err := r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := child.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		child.Close()
		return nil, fmt.Errorf("directory changed: %s/%s", r.Name(), name)
	}
	return child, nil
}

func CheckAncestors(path string) error {
	r, err := OpenDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	return r.Close()
}

func ReadDir(path string) ([]os.DirEntry, error) {
	r, err := OpenDir(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return entries(r)
}

func entries(r *os.Root) ([]os.DirEntry, error) {
	f, err := r.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ents, err := f.ReadDir(-1)
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	return ents, err
}

// Measure fails on unreadable trees or nested mounts instead of undercounting.
// The selected root may itself be a volume; descendants may not be mounts.
func Measure(path string) (int64, error) { return MeasureContext(context.Background(), path) }

func MeasureContext(ctx context.Context, path string) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	parent, err := OpenDir(filepath.Dir(path))
	if err != nil {
		return 0, err
	}
	defer parent.Close()
	return walkContext(ctx, parent, filepath.Base(path), nil, nil, false)
}

// Remove removes a single child tree. It refuses mounted roots as well as
// nested mounts, checks the remaining budget at each unlink, and never chmods
// regular files (which may have hardlinks outside the removal tree).
func Remove(path string, remaining *int64) (int64, error) {
	return RemoveContext(context.Background(), path, remaining)
}

func RemoveContext(ctx context.Context, path string, remaining *int64) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	parent, err := OpenDir(filepath.Dir(path))
	if err != nil {
		return 0, err
	}
	defer parent.Close()
	boundary, err := identity(parent)
	if err != nil {
		return 0, err
	}
	return walkContext(ctx, parent, filepath.Base(path), &boundary, remaining, true)
}

func walk(parent *os.Root, name string, boundary *mountIdentity, remaining *int64, remove bool) (int64, error) {
	return walkContext(context.Background(), parent, name, boundary, remaining, remove)
}
func walkContext(ctx context.Context, parent *os.Root, name string, boundary *mountIdentity, remaining *int64, remove bool) (int64, error) {
	if err := worklimit.Step(ctx); err != nil {
		return 0, err
	}
	fi, err := parent.Lstat(name)
	if err != nil {
		return 0, err
	}
	var total int64
	if fi.IsDir() {
		child, err := openChild(parent, name)
		if err != nil {
			return 0, err
		}
		defer child.Close()
		id, err := identity(child)
		if err != nil {
			return 0, err
		}
		if boundary != nil && id != *boundary {
			return 0, fmt.Errorf("refusing mount boundary at %s/%s", parent.Name(), name)
		}
		if remove && fi.Mode().Perm()&0o200 == 0 {
			f, err := child.Open(".")
			if err != nil {
				return 0, err
			}
			err = f.Chmod(fi.Mode().Perm() | 0o200)
			f.Close()
			if err != nil {
				return 0, err
			}
		}
		ents, err := entries(child)
		if err != nil {
			return 0, err
		}
		for _, de := range ents {
			n, err := walkContext(ctx, child, de.Name(), &id, remaining, remove)
			total += n
			if err != nil {
				return total, err
			}
		}
	}
	// Check identity again before unlinking; Remove only unlinks a file/link or
	// an empty directory, so newly-created contents cannot be recursively swept up.
	current, err := parent.Lstat(name)
	if err != nil {
		return total, err
	}
	if !os.SameFile(fi, current) {
		return total, fmt.Errorf("path changed: %s/%s", parent.Name(), name)
	}
	b := size.Allocated(current)
	if remove {
		if remaining != nil && b > *remaining {
			return total, fmt.Errorf("deletion budget exhausted at %s/%s", parent.Name(), name)
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if err := parent.Remove(name); err != nil {
			return total, err
		}
		if remaining != nil {
			*remaining -= b
		}
	}
	return total + b, nil
}
