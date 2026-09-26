// Package mutation serializes cooperating oos writers. The stable lock inode
// is never unlinked: doing so would allow two different inodes to be locked.
package mutation

import (
	"fmt"
	"github.com/afterdarksys/oos/internal/safefs"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

type Lock struct{ f *os.File }

func Acquire(home string) (*Lock, error) {
	if home == "" || !filepath.IsAbs(home) {
		return nil, fmt.Errorf("mutation lock requires an absolute home")
	}
	dir := filepath.Join(home, ".local", "state", "oos")
	if err := safefs.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	r, err := safefs.OpenDir(dir)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.OpenFile("mutation.lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("invalid mutation lock file")
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another oos mutation is running: %w", err)
	}
	return &Lock{f: f}, nil
}
func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
