package reserve

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestEnsureWritesRealBytesAndRelease(t *testing.T) {
	old, oldMin := Size, MinFree
	Size, MinFree = 256<<10, 0
	defer func() { Size, MinFree = old, oldMin }()
	p := Path(filepath.Join(t.TempDir(), "state", "state.json"))
	if err := Ensure(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil || fi.Size() != Size {
		t.Fatalf("reserve: %v %v", fi, err)
	}
	if st := fi.Sys().(*syscall.Stat_t); int64(st.Blocks)*512 < Size {
		t.Fatalf("reserve is sparse: %d blocks", st.Blocks)
	}
	if err := Ensure(p); err != nil {
		t.Fatal(err)
	}
	if ok, err := Release(p); !ok || err != nil {
		t.Fatalf("release: %v %v", ok, err)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatal("reserve still present")
	}
	if ok, err := Release(p); ok || err != nil {
		t.Fatalf("second release: %v %v", ok, err)
	}
}

func TestEnsureSkipsWhenSpaceIsLow(t *testing.T) {
	oldMin := MinFree
	MinFree = ^uint64(0)
	defer func() { MinFree = oldMin }()
	p := Path(filepath.Join(t.TempDir(), "state.json"))
	if err := Ensure(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatal("reserve created on a nearly full volume")
	}
}

func TestPathNeedsAbsoluteStateFile(t *testing.T) {
	if Path("") != "" || Path("rel/state.json") != "" {
		t.Fatal("relative state file produced a reserve path")
	}
}
