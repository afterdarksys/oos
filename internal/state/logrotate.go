package state

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// Size-based rotation for oos.log and daemon.log. Tests shrink these.
var (
	LogRotateBytes int64 = 50 << 20 // rotate once a log reaches this size
	LogKeep              = 3        // rotated generations kept: path.1 .. path.N
)

// rotate shifts path.N-1 -> path.N ... path -> path.1 under an exclusive
// flock on path+".rotate.lock", re-checking the size once the lock is held so
// two processes do not both rotate. Every step is one rename: a crash leaves
// each line in exactly one file, and a writer that still holds the old
// descriptor keeps appending to path.1, so no audit line is lost. The next
// OpenFile(O_CREATE) starts a fresh path.
func rotate(path string) error {
	lf, err := os.OpenFile(path+".rotate.lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := lockWithDeadline(lf, LockWait); err != nil {
		return err
	}
	defer unix.Flock(int(lf.Fd()), unix.LOCK_UN)
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() < LogRotateBytes {
		return err
	}
	keep := LogKeep
	if keep < 1 {
		keep = 1
	}
	for i := keep - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", path, i)
		if _, err := os.Lstat(src); err == nil {
			if err := os.Rename(src, fmt.Sprintf("%s.%d", path, i+1)); err != nil {
				return err
			}
		}
	}
	return os.Rename(path, path+".1")
}

// RotatingLog is an append-only log for a long-lived writer (the daemon):
// before each write it checks the size and, at LogRotateBytes, rotates and
// reopens. A line is written whole to one file.
type RotatingLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// OpenRotatingLog opens path through OpenLog.
func OpenRotatingLog(path string) (*RotatingLog, error) {
	f, err := OpenLog(path)
	if err != nil {
		return nil, err
	}
	return &RotatingLog{path: path, f: f}, nil
}

// File is the currently open file (for os.SameFile comparisons).
func (l *RotatingLog) File() *os.File {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f
}

func (l *RotatingLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, os.ErrClosed
	}
	if fi, err := l.f.Stat(); err == nil && LogRotateBytes > 0 && fi.Size() >= LogRotateBytes {
		if nf, err := OpenLog(l.path); err == nil {
			l.f.Close()
			l.f = nf
		}
	}
	return l.f.Write(b)
}

func (l *RotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
