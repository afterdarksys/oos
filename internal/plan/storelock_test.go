package plan

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func storeUID(t *testing.T, p string) uint32 {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Uid
}

// newStore returns a store dir, owned by another user when running as root.
func newStore(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "q")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, 54321, 54321); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A store lock hardlinked to a victim file is refused and the victim keeps
// its owner (as root this is the /etc/shadow chown escalation).
func TestStoreLockRefusesHardlink(t *testing.T) {
	dir := newStore(t)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := storeUID(t, victim)
	if err := os.Link(victim, filepath.Join(dir, storeLockName)); err != nil {
		t.Fatal(err)
	}
	if rel, err := lockStore(dir); err == nil {
		rel()
		t.Fatal("hardlinked store lock accepted")
	}
	if got := storeUID(t, victim); got != before {
		t.Fatalf("victim chowned from %d to %d", before, got)
	}
}

func TestStoreLockRefusesSymlink(t *testing.T) {
	dir := newStore(t)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, storeLockName)); err != nil {
		t.Fatal(err)
	}
	if rel, err := lockStore(dir); err == nil {
		rel()
		t.Fatal("symlinked store lock accepted")
	}
}

func TestStoreLockRefusesForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	dir := newStore(t)
	p := filepath.Join(dir, storeLockName)
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(p, 777, 777); err != nil {
		t.Fatal(err)
	}
	if rel, err := lockStore(dir); err == nil {
		rel()
		t.Fatal("foreign-owned store lock accepted")
	}
	if got := storeUID(t, p); got != 777 {
		t.Fatalf("foreign lock chowned to %d", got)
	}
}

// Root creating a fresh lock hands it to the store owner; an existing lock
// root owns is reused without a chown.
func TestStoreLockRootCreateChowns(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	dir := newStore(t)
	rel, err := lockStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	p := filepath.Join(dir, storeLockName)
	if got := storeUID(t, p); got != 54321 {
		t.Fatalf("fresh lock owned by %d, want store owner", got)
	}
	dir2 := newStore(t)
	p2 := filepath.Join(dir2, storeLockName)
	if err := os.WriteFile(p2, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if rel, err = lockStore(dir2); err != nil {
		t.Fatal(err)
	}
	rel()
	if got := storeUID(t, p2); got != 0 {
		t.Fatalf("existing lock chowned to %d", got)
	}
}
