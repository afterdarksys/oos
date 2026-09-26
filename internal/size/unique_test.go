package size

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/testutil"
)

// TestUniqueCountsHardlinksOnce: two names for one inode are one removal's
// worth of blocks. Allocated still reports both, which is what du does.
func TestUniqueCountsHardlinksOnce(t *testing.T) {
	home := t.TempDir()
	a := filepath.Join(home, "d", "a")
	testutil.Write(t, a, 300<<10)
	if err := os.Link(a, filepath.Join(home, "d", "b")); err != nil {
		t.Skip("hardlinks unsupported here:", err)
	}
	alloc, uniq, err := Unique(filepath.Join(home, "d"))
	if err != nil {
		t.Fatal(err)
	}
	if alloc < 2*(300<<10) {
		t.Fatalf("allocated should count both names: %d", alloc)
	}
	if uniq >= alloc || uniq < 300<<10 {
		t.Fatalf("unique should count the inode once: unique=%d allocated=%d", uniq, alloc)
	}
}

// A clone with a modified tail may share its first block. Whole-file sharing
// cannot be inferred from that block, so macOS reports an allocated upper bound.
func TestClonesUseConservativeUpperEstimate(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("APFS clone fixture")
	}
	home := t.TempDir()
	src := filepath.Join(home, "source")
	dst := filepath.Join(home, "clone")
	testutil.Write(t, src, 400<<10)
	if out, err := exec.Command("cp", "-c", src, dst).CombinedOutput(); err != nil {
		t.Skipf("clones unavailable: %v %s", err, out)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte("different tail"), 300<<10)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	alloc, upper, err := Unique(home)
	if err != nil {
		t.Fatal(err)
	}
	if upper != alloc {
		t.Fatalf("first-block inference remained: allocated=%d upper=%d", alloc, upper)
	}
}

// TestUniqueAgainstKeptOrigin: removing clones whose origin stays returns
// nothing for the shared blocks. Hardlinks behave the same on every OS, so
// that half of the test always runs.
func TestUniqueAgainstKeptOrigin(t *testing.T) {
	home := t.TempDir()
	keep := filepath.Join(home, "keep", "a")
	testutil.Write(t, keep, 300<<10)
	stale := filepath.Join(home, "stale")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(keep, filepath.Join(stale, "link")); err != nil {
		t.Skip("hardlinks unsupported here:", err)
	}
	alloc, uniq, err := UniqueAgainst([]string{filepath.Join(home, "keep")}, stale)
	if err != nil {
		t.Fatal(err)
	}
	if alloc < 300<<10 {
		t.Fatalf("allocated should still record the link: %d", alloc)
	}
	if uniq >= 300<<10 {
		t.Fatalf("a link to a kept file returns nothing: unique=%d", uniq)
	}
	if runtime.GOOS != "darwin" {
		return
	}
	if err := exec.Command("cp", "-c", keep, filepath.Join(stale, "clone")).Run(); err != nil {
		t.Skip("cp -c unavailable:", err)
	}
	_, uniq, _ = UniqueAgainst([]string{filepath.Join(home, "keep")}, stale)
	if uniq < 300<<10 {
		t.Errorf("clone ownership is unknown and must retain an upper estimate: %d", uniq)
	}
}

// TestCacheUniqueServesWhileAllocatedUnchanged: the clone-aware walk is the
// expensive one, so its answer is kept as long as the ordinary allocated
// total for the same key has not moved and the TTL has not passed. A change
// in allocated bytes means the tree changed, and the answer is remeasured.
func TestCacheUniqueServesWhileAllocatedUnchanged(t *testing.T) {
	home := t.TempDir()
	c := OpenCacheWith(filepath.Join(home, "sizes.json"), time.Hour, 0)
	calls := 0
	compute := func() (int64, error) { calls++; return 10, nil }
	now := time.Now()
	if u, ok := c.Unique("k", 100, now, compute); !ok || u != 10 || calls != 1 {
		t.Fatalf("first: u=%d ok=%v calls=%d", u, ok, calls)
	}
	if u, ok := c.Unique("k", 100, now.Add(time.Minute), compute); !ok || u != 10 || calls != 1 {
		t.Fatalf("warm: u=%d ok=%v calls=%d", u, ok, calls)
	}
	c.Unique("k", 101, now.Add(time.Minute), compute)
	if calls != 2 {
		t.Fatalf("changed allocated must remeasure: calls=%d", calls)
	}
	c.Unique("k", 101, now.Add(2*time.Hour), compute)
	if calls != 3 {
		t.Fatalf("expired must remeasure: calls=%d", calls)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2 := OpenCacheWith(filepath.Join(home, "sizes.json"), time.Hour, 0)
	calls2 := 0
	if u, ok := c2.Unique("k", 101, now.Add(2*time.Hour+time.Minute), func() (int64, error) { calls2++; return 99, nil }); !ok || u != 10 || calls2 != 0 {
		t.Fatalf("reloaded cache should serve: u=%d ok=%v calls=%d", u, ok, calls2)
	}
	off := OpenCache("", time.Hour)
	if u, ok := off.Unique("k", 1, now, compute); !ok || u != 10 || calls != 4 {
		t.Fatalf("disabled cache computes every time: u=%d ok=%v calls=%d", u, ok, calls)
	}
}
