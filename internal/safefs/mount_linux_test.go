package safefs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/afterdarksys/oos/internal/testutil"
)

func TestLinuxBindMountBoundary(t *testing.T) {
	marker, markerErr := os.ReadFile("/etc/oos-disposable-vm")
	if os.Getenv("OOS_DISPOSABLE_VM") != "1" || markerErr != nil || string(marker) != "OOS-DISPOSABLE-VM-V1\n" {
		t.Skip("requires disposable VM opt-in")
	}
	home := t.TempDir()
	source := filepath.Join(home, "source")
	tree := filepath.Join(home, "tree")
	mount := filepath.Join(tree, "mounted")
	testutil.Write(t, filepath.Join(source, "valuable"), 4096)
	if err := os.MkdirAll(mount, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mount(source, mount, "", syscall.MS_BIND, ""); err != nil {
		t.Skipf("bind-mount fixture requires CAP_SYS_ADMIN: %v", err)
	}
	defer syscall.Unmount(mount, 0)
	if _, err := Measure(tree); err == nil {
		t.Fatal("same-device bind mount counted as ordinary tree")
	}
	if _, err := Remove(tree, nil); err == nil {
		t.Fatal("same-device bind mount removed")
	}
	if _, err := os.Stat(filepath.Join(source, "valuable")); err != nil {
		t.Fatal("bind-mounted file deleted", err)
	}
}
