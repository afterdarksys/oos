package trash

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/afterdarksys/oos/internal/testutil"
)

func TestAllowed(t *testing.T) {
	old := VolumeRoot
	VolumeRoot = "/Volumes"
	t.Cleanup(func() { VolumeRoot = old })
	home := "/Users/ryan"
	if !Allowed(home+"/.Trash", home, 501) {
		t.Fatal("home trash")
	}
	if !Allowed(home+"/.local/share/Trash", home, 501) {
		t.Fatal("freedesktop trash")
	}
	if !Allowed("/Volumes/Backup/.Trashes/501", home, 501) {
		t.Fatal("volume trash")
	}
	for _, bad := range []string{"/", "/tmp", "/Volumes/.Trashes/501", "/Volumes/Backup/.Trashes/0", home + "/.Trash/nope", "/Volumes/Backup/../../etc/.Trashes/501"} {
		if Allowed(bad, home, 501) {
			t.Errorf("allowed %s", bad)
		}
	}
}

func TestEmptyLeavesTheBinAndDoesNotFollowLinks(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".Trash")
	testutil.Write(t, filepath.Join(bin, "gone.txt"), 100)
	testutil.Write(t, filepath.Join(bin, "dir", "a"), 50)
	outside := filepath.Join(home, "keep.txt")
	testutil.Write(t, outside, 20)
	if err := os.Symlink(outside, filepath.Join(bin, "link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Empty(filepath.Join(home, "not-trash"), home, 501); err == nil {
		t.Fatal("a random directory must be refused")
	}
	bytes, n, err := Empty(bin, home, 501)
	if err != nil || n != 3 || bytes < 150 {
		t.Fatalf("empty: bytes=%d n=%d err=%v", bytes, n, err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatal("the bin directory must stay")
	}
	if _, err := os.Stat(filepath.Join(bin, "gone.txt")); !os.IsNotExist(err) {
		t.Fatal("contents must be gone")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("a symlink target outside the bin must survive")
	}
	linkBin := filepath.Join(home, "linkbin")
	if err := os.Symlink(bin, linkBin); err != nil {
		t.Fatal(err)
	}
	// Allowed would accept only the real home trash path, not this name.
	if _, _, err := Empty(linkBin, home, 501); err == nil {
		t.Fatal("a path that is not a bin must be refused")
	}
}

func TestFreedesktopEmptyKeepsTheDirectories(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".local", "share", "Trash")
	testutil.Write(t, filepath.Join(root, "files", "a"), 30)
	testutil.Write(t, filepath.Join(root, "info", "a.trashinfo"), 10)
	if _, n, err := Empty(root, home, 501); err != nil || n != 2 {
		t.Fatalf("empty: n=%d err=%v", n, err)
	}
	for _, d := range []string{"files", "info"} {
		if fi, err := os.Stat(filepath.Join(root, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s should remain: %v", d, err)
		}
	}
}

func TestLocateHomeAndVolume(t *testing.T) {
	home := t.TempDir()
	vols := t.TempDir()
	old := VolumeRoot
	VolumeRoot = vols
	t.Cleanup(func() { VolumeRoot = old })
	testutil.Write(t, filepath.Join(home, ".Trash", "a"), 100)
	testutil.Write(t, filepath.Join(home, ".local", "share", "Trash", "files", "a"), 100)
	uid := os.Getuid()
	testutil.Write(t, filepath.Join(vols, "Backup", ".Trashes", strconv.Itoa(uid), "b"), 200)
	// a wrong uid is not this user's trash
	testutil.Write(t, filepath.Join(vols, "Other", ".Trashes", strconv.Itoa(uid+1), "c"), 200)
	bins := locate(home, uid)
	if runtime.GOOS != "darwin" {
		if len(bins) != 1 || bins[0].Kind != "freedesktop" || bins[0].Entries != 1 {
			t.Fatalf("freedesktop bins: %+v", bins)
		}
		return
	}
	if len(bins) != 2 {
		t.Fatalf("bins: %+v", bins)
	}
	if bins[0].Kind != "home" || bins[0].Entries != 1 || bins[1].Kind != "volume" || bins[1].Entries != 1 {
		t.Fatalf("bins: %+v", bins)
	}
}

func TestFreedesktopRefusesSymlinkFilesDirectory(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "share", "Trash")
	outside := filepath.Join(home, "valuable")
	testutil.Write(t, filepath.Join(outside, "data"), 4096)
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(bin, "files")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Empty(bin, home, os.Getuid()); err == nil {
		t.Fatal("symlink files directory accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "data")); err != nil {
		t.Fatal(err)
	}
}
