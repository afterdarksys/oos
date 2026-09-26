package safefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestRemoveBudgetAndHardlinkPermissions(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "cache")
	outside := filepath.Join(home, "kept")
	testutil.Write(t, outside, 4096)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o444); err != nil {
		t.Fatal(err)
	}
	remaining := int64(size.GB)
	if _, err := Remove(root, &remaining); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o444 {
		t.Fatalf("changed hardlinked file permissions: %v", fi.Mode())
	}
	testutil.Write(t, filepath.Join(root, "large"), 8192)
	remaining = 1
	if _, err := Remove(root, &remaining); err == nil {
		t.Fatal("budget accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "large")); err != nil {
		t.Fatal("over-budget file removed", err)
	}
}

func TestBoundaryRefusedBeforeChmodOrDeletion(t *testing.T) {
	home := t.TempDir()
	child := filepath.Join(home, "child")
	testutil.Write(t, filepath.Join(child, "valuable"), 4096)
	if err := os.Chmod(child, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(child, 0o755)
	r, err := OpenDir(home)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	wrong, err := identity(r)
	if err != nil {
		t.Fatal(err)
	}
	wrong.device++
	if _, err := walk(r, "child", &wrong, nil, true); err == nil {
		t.Fatal("mount boundary accepted")
	}
	fi, err := os.Stat(child)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o555 {
		t.Fatal("chmod before mount check")
	}
	if _, err := os.Stat(filepath.Join(child, "valuable")); err != nil {
		t.Fatal(err)
	}
}

func TestOpenDirAndRemoveRefuseSymlinkAncestors(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "valuable")
	testutil.Write(t, target, 4096)
	if err := os.Symlink(outside, filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDir(filepath.Join(home, "alias")); err == nil {
		t.Fatal("alias accepted")
	}
	if _, err := Remove(filepath.Join(home, "alias", "valuable"), nil); err == nil {
		t.Fatal("alias removal accepted")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
	// A link inside a selected tree is unlinked without touching its target.
	if _, err := Remove(filepath.Join(home, "alias"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
}
