package safefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// ErrNoReplaceUnsupported is wrapped when the filesystem rejects the atomic
// no-replace rename. oos never falls back to check-then-rename, so quarantine
// is unavailable on such a volume.
var ErrNoReplaceUnsupported = errors.New("filesystem does not support no-replace rename; quarantine unavailable on this volume")

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
		if noReplaceUnsupported(err) {
			return fmt.Errorf("rename %s -> %s: %w (%w)", src, dst, ErrNoReplaceUnsupported, err)
		}
		return &os.LinkError{Op: "rename-noreplace", Old: src, New: dst, Err: err}
	}
	return nil
}

func noReplaceUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP)
}
