// Package reserve keeps a small preallocated file next to oos's state so a
// full disk cannot stop oos from writing the audit line that must precede a
// permanent delete: on ENOSPC the reserve is unlinked and the write retried.
package reserve

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/afterdarksys/oos/internal/safefs"
	"github.com/afterdarksys/oos/internal/size"
	"golang.org/x/sys/unix"
)

// Name is the reserve file's name inside the state directory.
const Name = ".oos-reserve"

// Size and MinFree are variables for package tests only.
var (
	Size    int64  = 8 << 20
	MinFree uint64 = 1 << 30
)

// Path returns the reserve for a state file ("" when there is none).
func Path(stateFile string) string {
	if stateFile == "" || !filepath.IsAbs(stateFile) {
		return ""
	}
	return filepath.Join(filepath.Dir(stateFile), Name)
}

// Ensure creates or refills the reserve when it is missing or short and the
// volume has more than MinFree available. The bytes are random and written
// out, so neither sparse files nor compression make the reservation empty.
func Ensure(path string) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := safefs.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	r, err := safefs.OpenDir(dir)
	if err != nil {
		return err
	}
	defer r.Close()
	if fi, err := r.Lstat(Name); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("space reserve %s is not a regular file", path)
		}
		if fi.Size() >= Size {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	du, err := size.Disk(dir)
	if err != nil {
		return err
	}
	if du.Free <= MinFree {
		return nil
	}
	tmp := Name + ".tmp"
	f, err := r.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = io.CopyN(f, rand.Reader, Size)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	d, derr := r.Open(".")
	if derr != nil {
		_ = r.Remove(tmp)
		return errors.Join(err, derr)
	}
	defer d.Close()
	if err == nil {
		err = unix.Renameat(int(d.Fd()), tmp, int(d.Fd()), Name)
	}
	if err != nil {
		_ = r.Remove(tmp)
		return fmt.Errorf("space reserve %s: %w", path, err)
	}
	return d.Sync()
}

// Release unlinks the reserve and syncs its directory so the blocks are
// really free before the caller retries. It reports whether a reserve was
// removed.
func Release(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	r, err := safefs.OpenDir(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	defer r.Close()
	fi, err := r.Lstat(Name)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("space reserve %s is not a regular file", path)
	}
	if err := r.Remove(Name); err != nil {
		return false, err
	}
	d, err := r.Open(".")
	if err != nil {
		return true, err
	}
	defer d.Close()
	return true, d.Sync()
}
