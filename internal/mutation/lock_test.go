package mutation

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestExclusiveMutationLock(t *testing.T) {
	home := t.TempDir()
	first, err := Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(home)
	if err == nil {
		second.Close()
		t.Fatal("concurrent writer accepted")
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("busy lock must wrap ErrBusy: %v", err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}

// Root acting on a user's home leaves the lock and its new parents owned by
// that user, so the user's own oos can still lock.
func TestRootLockOwnedByHomeOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	home := t.TempDir()
	const uid = 54321
	if err := os.Chown(home, uid, uid); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	for _, p := range []string{".local", ".local/state", ".local/state/oos", ".local/state/oos/mutation.lock"} {
		fi, err := os.Lstat(filepath.Join(home, p))
		if err != nil {
			t.Fatal(err)
		}
		if st := fi.Sys().(*syscall.Stat_t); st.Uid != uid {
			t.Errorf("%s owned by %d, want %d", p, st.Uid, uid)
		}
	}
}

func lockPath(home string) string {
	return filepath.Join(home, ".local", "state", "oos", "mutation.lock")
}

func mkStateDir(t *testing.T, home string, uid int) string {
	t.Helper()
	d := filepath.Join(home, ".local", "state", "oos")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if uid >= 0 {
		for _, p := range []string{home, filepath.Join(home, ".local"), filepath.Join(home, ".local", "state"), d} {
			if err := os.Chown(p, uid, uid); err != nil {
				t.Fatal(err)
			}
		}
	}
	return d
}

func uidOf(t *testing.T, p string) uint32 {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Uid
}

// A lock name hardlinked to another file (as root: a root-owned victim like
// /etc/shadow) is refused and the victim's owner is never changed.
func TestLockRefusesHardlink(t *testing.T) {
	home := t.TempDir()
	owner := -1
	if os.Geteuid() == 0 {
		owner = 54321
	}
	mkStateDir(t, home, owner)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := uidOf(t, victim)
	if err := os.Link(victim, lockPath(home)); err != nil {
		t.Fatal(err)
	}
	if l, err := Acquire(home); err == nil {
		l.Close()
		t.Fatal("hardlinked lock accepted")
	}
	if got := uidOf(t, victim); got != before {
		t.Fatalf("victim chowned from %d to %d", before, got)
	}
}

func TestLockRefusesSymlink(t *testing.T) {
	home := t.TempDir()
	mkStateDir(t, home, -1)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, lockPath(home)); err != nil {
		t.Fatal(err)
	}
	if l, err := Acquire(home); err == nil {
		l.Close()
		t.Fatal("symlinked lock accepted")
	}
}

// A lock owned by neither us, the directory owner nor the home owner is
// refused and left as it is.
func TestLockRefusesForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	home := t.TempDir()
	mkStateDir(t, home, 54321)
	if err := os.WriteFile(lockPath(home), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(lockPath(home), 777, 777); err != nil {
		t.Fatal(err)
	}
	if l, err := Acquire(home); err == nil {
		l.Close()
		t.Fatal("foreign-owned lock accepted")
	}
	if got := uidOf(t, lockPath(home)); got != 777 {
		t.Fatalf("foreign lock chowned to %d", got)
	}
}

// An existing lock is reused, never chowned.
func TestLockExistingNotChowned(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	home := t.TempDir()
	mkStateDir(t, home, 54321)
	if err := os.WriteFile(lockPath(home), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if got := uidOf(t, lockPath(home)); got != 0 {
		t.Fatalf("existing root-owned lock chowned to %d", got)
	}
}
