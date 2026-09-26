//go:build linux && vmtest

package size

import (
	"context"
	"github.com/afterdarksys/oos/internal/testutil"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func requireExtentVM(t *testing.T) {
	t.Helper()
	b, e := os.ReadFile("/etc/oos-disposable-vm")
	if e != nil || string(b) != "OOS-DISPOSABLE-VM-V1\n" || os.Getenv("OOS_DISPOSABLE_VM") != "1" {
		t.Skip("requires disposable VM marker")
	}
}
func TestVMReflinkAccounting(t *testing.T) {
	requireExtentVM(t)
	root := t.TempDir()
	caps, err := Filesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Filesystem != "xfs" && caps.Filesystem != "btrfs" {
		t.Skip("filesystem has no required reflink fixture")
	}
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "clone")
	testutil.Write(t, src, 4<<20)
	a, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err = unix.IoctlFileClone(int(b.Fd()), int(a.Fd())); err != nil {
		t.Fatalf("required reflink support unavailable: %v", err)
	}
	if _, err = b.WriteAt([]byte("modified clone tail"), 3<<20); err != nil {
		t.Fatal(err)
	}
	if err = b.Sync(); err != nil {
		t.Fatal(err)
	}
	report, err := Account(context.Background(), nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Shared == 0 || report.Exclusive == 0 || report.Unknown != 0 {
		t.Fatalf("partial clone extents not identified: %+v", report)
	}
}
func TestVMBtrfsSnapshotRetention(t *testing.T) {
	requireExtentVM(t)
	root := t.TempDir()
	caps, err := Filesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Filesystem != "btrfs" {
		t.Skip("btrfs-only snapshot fixture")
	}
	source, snapshot := filepath.Join(root, "source"), filepath.Join(root, "snapshot")
	run := func(args ...string) {
		t.Helper()
		if b, e := exec.Command("btrfs", args...).CombinedOutput(); e != nil {
			t.Fatalf("btrfs %v: %v %s", args, e, b)
		}
	}
	run("subvolume", "create", source)
	t.Cleanup(func() {
		exec.Command("btrfs", "subvolume", "delete", snapshot).Run()
		exec.Command("btrfs", "subvolume", "delete", source).Run()
	})
	file := filepath.Join(source, "data")
	testutil.Write(t, file, 4<<20)
	f, e := os.OpenFile(file, os.O_RDWR, 0)
	if e != nil {
		t.Fatal(e)
	}
	f.Sync()
	f.Close()
	run("subvolume", "snapshot", "-r", source, snapshot)
	report, err := Account(context.Background(), nil, file)
	if err != nil {
		t.Fatal(err)
	}
	if report.Shared == 0 || report.Exclusive != 0 {
		t.Fatalf("snapshot sharing not reported: %+v", report)
	}
}
