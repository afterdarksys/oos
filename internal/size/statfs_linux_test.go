//go:build linux

package size

import (
	"syscall"
	"testing"
)

func TestBlockSizePrefersFragmentSize(t *testing.T) {
	st := syscall.Statfs_t{Bsize: 1 << 20, Frsize: 4096}
	if got := blockSize(&st); got != 4096 {
		t.Fatalf("f_blocks counts fragments: got %d, want 4096", got)
	}
	st.Frsize = 0
	if got := blockSize(&st); got != 1<<20 {
		t.Fatalf("zero f_frsize falls back to f_bsize: got %d", got)
	}
}

func TestDiskUsesFragmentSize(t *testing.T) {
	dir := t.TempDir()
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		t.Fatal(err)
	}
	du, err := Disk(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := st.Blocks * blockSize(&st); du.Total != want {
		t.Fatalf("total %d, want blocks*frsize %d", du.Total, want)
	}
}
