// Package mutation serializes cooperating oos writers. The stable lock inode
// is never unlinked: doing so would allow two different inodes to be locked.
package mutation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/afterdarksys/oos/internal/safefs"
	"golang.org/x/sys/unix"
)

// ErrBusy is wrapped into the error returned when another process holds an
// oos lock (the mutation lock, or a quarantine store lock).
var ErrBusy = errors.New("another oos mutation is running")

type Lock struct{ f *os.File }

// geteuid is replaced in package tests only.
var geteuid = os.Geteuid

func Acquire(home string) (*Lock, error) {
	if home == "" || !filepath.IsAbs(home) {
		return nil, fmt.Errorf("mutation lock requires an absolute home")
	}
	if err := safefs.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	hr, err := safefs.OpenDir(home)
	if err != nil {
		return nil, err
	}
	defer hr.Close()
	hd, err := hr.Open(".")
	if err != nil {
		return nil, err
	}
	defer hd.Close()
	// Root acting on another user's home must not leave root-owned state
	// behind, or that user's own oos can no longer take the lock.
	owner := -1
	if geteuid() == 0 {
		var st unix.Stat_t
		if err := unix.Fstat(int(hd.Fd()), &st); err == nil && st.Uid != 0 {
			owner = int(st.Uid)
		}
	}
	dir := filepath.Join(home, ".local", "state", "oos")
	df, err := mkdirOwnedAt(int(hd.Fd()), home, []string{".local", "state", "oos"}, owner)
	if err != nil {
		return nil, err
	}
	defer df.Close()
	f, err := OpenLockFileAt(int(df.Fd()), dir, "mutation.lock", unix.O_RDWR, 0o600, owner)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (lock %s): %w", ErrBusy, filepath.Join(dir, "mutation.lock"), err)
		}
		return nil, fmt.Errorf("mutation lock: %w", err)
	}
	return &Lock{f: f}, nil
}

// mkdirOwnedAt walks parts below the directory fd base, creating missing
// components, and returns the last one opened. A directory is chowned to
// owner (when >= 0) only if this call created it with mkdirat, and only by
// the fd opened right after, after checking that fd is still an empty
// directory owned by us: a user who owns the parent can swap the new entry
// for a symlink (O_NOFOLLOW refuses it) or another inode (the checks refuse
// it), so the chown never lands on a file the user planted.
func mkdirOwnedAt(base int, basePath string, parts []string, owner int) (*os.File, error) {
	fd, err := unix.Dup(base)
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	path := basePath
	for _, part := range parts {
		path = filepath.Join(path, part)
		created := false
		if err := unix.Mkdirat(fd, part, 0o700); err == nil {
			created = true
		} else if !errors.Is(err, unix.EEXIST) {
			unix.Close(fd)
			return nil, fmt.Errorf("mkdir %s: %w", path, err)
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		fd = next
		if created && owner >= 0 {
			var st unix.Stat_t
			if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != geteuid() || st.Nlink > 2 {
				unix.Close(fd)
				return nil, fmt.Errorf("%s changed while it was being created; refusing to chown it", path)
			}
			if err := unix.Fchown(fd, owner, -1); err != nil {
				unix.Close(fd)
				return nil, fmt.Errorf("chown %s to home owner %d: %w", path, owner, err)
			}
		}
	}
	return os.NewFile(uintptr(fd), path), nil
}

// OpenLockFileAt opens the lock file name in the directory fd dirfd (dirPath
// is only for messages), creating it when missing. flag is the access mode.
//
// Threats: root runs oos in directories other users own (a user's
// quarantine store, HOME kept by sudo). Such a user can plant the lock name
// as a symlink or as a hardlink to a file they cannot write (/etc/shadow;
// macOS has no protected_hardlinks), hoping root chowns or truncates it.
// So: an existing file is never chowned. The lock is created with
// O_CREAT|O_EXCL|O_NOFOLLOW, and only a file this call created is fchowned
// (by fd) to chownTo. An existing name is opened O_NOFOLLOW without O_CREAT
// and refused unless it is a regular file with one link owned by euid, the
// directory owner or chownTo. Refusals fail closed with an error.
func OpenLockFileAt(dirfd int, dirPath, name string, flag int, perm uint32, chownTo int) (*os.File, error) {
	path := filepath.Join(dirPath, name)
	const common = unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	fd, err := unix.Openat(dirfd, name, flag|common|unix.O_CREAT|unix.O_EXCL, perm)
	if err == nil {
		if chownTo >= 0 {
			var st unix.Stat_t
			if err := unix.Fstat(fd, &st); err != nil {
				unix.Close(fd)
				return nil, fmt.Errorf("lock %s: %w", path, err)
			}
			if int(st.Uid) != chownTo {
				if err := unix.Fchown(fd, chownTo, -1); err != nil {
					unix.Close(fd)
					return nil, fmt.Errorf("chown lock %s to %d: %w", path, chownTo, err)
				}
			}
		}
		return os.NewFile(uintptr(fd), path), nil
	}
	if !errors.Is(err, unix.EEXIST) {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	fd, err = unix.Openat(dirfd, name, flag|common, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("lock %s is a symlink; refusing it", path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	var st, dst unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := unix.Fstat(dirfd, &dst); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("lock dir %s: %w", dirPath, err)
	}
	switch uid := int(st.Uid); {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		err = fmt.Errorf("lock %s is not a regular file; refusing it", path)
	case st.Nlink != 1:
		err = fmt.Errorf("lock %s has %d hard links; refusing it (remove it if you did not create it)", path, st.Nlink)
	case uid != os.Geteuid() && uid != int(dst.Uid) && uid != chownTo:
		err = fmt.Errorf("lock %s is owned by uid %d, not by uid %d or the directory owner %d; refusing it", path, uid, os.Geteuid(), dst.Uid)
	}
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
