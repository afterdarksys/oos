package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/afterdarksys/oos/internal/mutation"
	"github.com/afterdarksys/oos/internal/safefs"
	"golang.org/x/sys/unix"
)

// storeLockName is flocked by every process that changes a quarantine store:
// take, restore, recover and purge. The root daemon and a user's CLI hold
// different mutation locks (one per HOME) but can share a store, so without
// it a purge could delete a batch mid-restore. Lock order: the mutation lock
// first, then store locks sorted by path. Within one process the lock is
// reference counted; the mutation lock already serializes that process.
const storeLockName = ".oos-store.lock"

// ErrStoreLock is wrapped into a store lock failure that is not a busy
// lock: the lock file is hardlinked, a symlink, foreign-owned or cannot be
// opened. The CLI maps it to exit 6 "io" whichever mutation hit it.
var ErrStoreLock = errors.New("quarantine store lock unusable")

// storeLockWait bounds the non-blocking retries; a variable for tests only.
var storeLockWait = 2 * time.Second

type heldStore struct {
	f    *os.File
	refs int
}

var (
	storeMu    sync.Mutex
	storeLocks = map[string]*heldStore{}
)

// lockStore takes the store lock for dir, which must exist.
func lockStore(dir string) (release func(), err error) {
	dir = filepath.Clean(dir)
	storeMu.Lock()
	defer storeMu.Unlock()
	if h := storeLocks[dir]; h != nil {
		h.refs++
		return func() { unlockStore(dir) }, nil
	}
	r, err := safefs.OpenDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoreLock, err)
	}
	defer r.Close()
	d, err := r.Open(".")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoreLock, err)
	}
	defer d.Close()
	// Root creating the lock in a store owned by someone else hands it over,
	// so that user can still open it. OpenLockFileAt only ever chowns a file
	// it just created (see its Threats note), never one planted in the store.
	chownTo := -1
	if geteuid() == 0 {
		var st unix.Stat_t
		if err := unix.Fstat(int(d.Fd()), &st); err == nil && st.Uid != 0 {
			chownTo = int(st.Uid)
		}
	}
	// O_RDONLY suffices for flock, so a lock file another user created
	// (root's daemon purging a user's store) still works.
	f, err := mutation.OpenLockFileAt(int(d.Fd()), dir, storeLockName, unix.O_RDONLY, 0o600, chownTo)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoreLock, err)
	}
	deadline := time.Now().Add(storeLockWait)
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			if errors.Is(err, unix.EWOULDBLOCK) {
				return nil, fmt.Errorf("quarantine store %s is in use by another oos process (restore, purge or cleanup); try again when it finishes: %w", dir, mutation.ErrBusy)
			}
			return nil, fmt.Errorf("%w: %s: %w", ErrStoreLock, dir, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	storeLocks[dir] = &heldStore{f: f, refs: 1}
	return func() { unlockStore(dir) }, nil
}

func unlockStore(dir string) {
	storeMu.Lock()
	defer storeMu.Unlock()
	h := storeLocks[dir]
	if h == nil {
		return
	}
	if h.refs--; h.refs <= 0 {
		h.f.Close()
		delete(storeLocks, dir)
	}
}

// lockStores takes the locks of every existing dir in path order.
func lockStores(dirs []string) (func(), error) {
	sorted := append([]string(nil), dirs...)
	sort.Strings(sorted)
	var releases []func()
	undo := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	seen := map[string]bool{}
	for _, d := range sorted {
		d = filepath.Clean(d)
		if seen[d] {
			continue
		}
		seen[d] = true
		if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
			continue
		}
		rel, err := lockStore(d)
		if err != nil {
			undo()
			return nil, err
		}
		releases = append(releases, rel)
	}
	return undo, nil
}
