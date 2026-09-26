package safefs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MkdirAll traverses through pinned directories, refusing symlink components.
func MkdirAll(path string, mode os.FileMode) error {
	path = CanonicalAlias(path)
	if !filepath.IsAbs(path) {
		return fmt.Errorf("not absolute: %s", path)
	}
	r, err := os.OpenRoot("/")
	if err != nil {
		return err
	}
	defer func() { r.Close() }()
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		if err := r.Mkdir(part, mode); err != nil && !os.IsExist(err) {
			return err
		}
		next, err := openChild(r, part)
		if err != nil {
			return err
		}
		r.Close()
		r = next
	}
	return nil
}

// MoveNoReplace never falls back to a check-then-rename sequence. Both parents
// remain pinned for the atomic, filesystem-supported no-replace operation.
func MoveNoReplace(src, dst string) error {
	a, err := OpenDir(filepath.Dir(src))
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := OpenDir(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer b.Close()
	af, err := a.Open(".")
	if err != nil {
		return err
	}
	defer af.Close()
	bf, err := b.Open(".")
	if err != nil {
		return err
	}
	defer bf.Close()
	if err := renameExclusive(int(af.Fd()), filepath.Base(src), int(bf.Fd()), filepath.Base(dst)); err != nil {
		return &os.LinkError{Op: "rename-noreplace", Old: src, New: dst, Err: err}
	}
	return nil
}
