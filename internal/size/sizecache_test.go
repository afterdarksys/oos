package size

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/testutil"
)

func mustLstat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func TestSizeCacheHitsInvalidatesAndExpires(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "d")
	testutil.Write(t, filepath.Join(dir, "sub", "a"), 65536)
	testutil.Write(t, filepath.Join(dir, "b"), 65536)
	c := OpenCacheWith(filepath.Join(home, "sizes.json"), time.Hour, 0) // test trees are tiny
	now := time.Now()
	fi, _ := os.Lstat(dir)
	dev, have := DeviceOf(fi)

	n1 := c.SizeDir(dir, dev, have, now)
	if n1 < 2*65536 {
		t.Fatalf("size too small: %d", n1)
	}
	h, m := c.Stats()
	if h != 0 || m == 0 {
		t.Fatalf("cold walk should miss: hits=%d misses=%d", h, m)
	}
	n2 := c.SizeDir(dir, dev, have, now.Add(time.Minute))
	h2, _ := c.Stats()
	if n2 != n1 || h2 == 0 {
		t.Fatalf("warm walk should hit the root: n2=%d hits=%d", n2, h2)
	}

	// adding a child bumps the parent mtime and invalidates the root but not sub
	time.Sleep(20 * time.Millisecond)
	testutil.Write(t, filepath.Join(dir, "c"), 65536)
	n3 := c.SizeDir(dir, dev, have, now.Add(2*time.Minute))
	if n3 <= n2 {
		t.Fatalf("new child not counted: %d <= %d", n3, n2)
	}

	// TTL expiry forces a recount
	before, _ := c.Stats()
	c.SizeDir(dir, dev, have, now.Add(3*time.Hour))
	after, _ := c.Stats()
	if after != before {
		t.Error("expired entries must not hit")
	}

	// save and reload round-trips
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2 := OpenCacheWith(filepath.Join(home, "sizes.json"), time.Hour, 0)
	if b, ok := c2.Get(dir, mustLstat(t, dir).ModTime(), now.Add(4*time.Minute)); !ok || b != n3 {
		t.Errorf("reloaded cache should serve the root: ok=%v b=%d want %d", ok, b, n3)
	}
	// disabled cache never hits
	off := OpenCache("", time.Hour)
	if _, ok := off.Get(dir, mustLstat(t, dir).ModTime(), now); ok {
		t.Error("disabled cache must not hit")
	}
}

// TestFreshRefreshesInsteadOfDisabling: a --fresh run must not serve a
// stored size, and must leave the cache holding what it measured, so the
// run after it is warm with honest numbers. Before 0.5.0 --fresh skipped
// the cache entirely and the stale entry survived it.
func TestFreshRefreshesInsteadOfDisabling(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "d")
	testutil.Write(t, filepath.Join(dir, "sub", "a"), 65536)
	file := filepath.Join(home, "sizes.json")
	now := time.Now()
	fi := mustLstat(t, dir)
	dev, have := DeviceOf(fi)

	// a lie in the cache: right mtime, fresh timestamp, wrong size
	c := OpenCacheWith(file, time.Hour, 0)
	c.Put(dir, fi.ModTime(), 1, now)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	warm := OpenCacheWith(file, time.Hour, 0)
	if n := warm.SizeDir(dir, dev, have, now.Add(time.Minute)); n != 1 {
		t.Fatalf("a normal run serves the stored size: got %d", n)
	}

	fresh := OpenCacheWith(file, time.Hour, 0)
	fresh.Refresh = true
	n := fresh.SizeDir(dir, dev, have, now.Add(2*time.Minute))
	if n < 65536 {
		t.Fatalf("--fresh must measure, got %d", n)
	}
	if h, _ := fresh.Stats(); h != 0 {
		t.Errorf("--fresh must never hit, got %d hits", h)
	}
	if err := fresh.Save(); err != nil {
		t.Fatal(err)
	}

	after := OpenCacheWith(file, time.Hour, 0)
	got, ok := after.Get(dir, fi.ModTime(), now.Add(3*time.Minute))
	if !ok || got != n {
		t.Errorf("the run after --fresh must be warm with the measured size: ok=%v got=%d want=%d", ok, got, n)
	}
}

func TestPathSizeCachedMatchesWalk(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "d")
	testutil.Write(t, filepath.Join(dir, "x", "y", "f"), 1<<20)
	testutil.Write(t, filepath.Join(dir, "g"), 1<<19)
	if err := os.Symlink(home, filepath.Join(dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	walk, _ := PathSizeWalk(dir)
	old := Active
	Active = OpenCacheWith(filepath.Join(home, "sizes.json"), time.Hour, 0)
	defer func() { Active = old }()
	cached, _ := PathSize(dir)
	if cached != walk {
		t.Errorf("cached %d != walk %d", cached, walk)
	}
	again, _ := PathSize(dir)
	if again != walk {
		t.Errorf("second cached read %d != walk %d", again, walk)
	}
}

func TestSizeCacheFloorSkipsSmallDirs(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "d")
	testutil.Write(t, filepath.Join(dir, "small", "f"), 1024)
	testutil.Write(t, filepath.Join(dir, "big", "f"), 8<<20)
	c := OpenCache(filepath.Join(home, "sizes.json"), time.Hour)
	fi, _ := os.Lstat(dir)
	dev, have := DeviceOf(fi)
	c.SizeDir(dir, dev, have, time.Now())
	if _, ok := c.entries[filepath.Join(dir, "small")]; ok {
		t.Error("directories under the floor must not be stored")
	}
	if _, ok := c.entries[filepath.Join(dir, "big")]; !ok {
		t.Error("directories over the floor must be stored")
	}
	if _, ok := c.entries[dir]; !ok {
		t.Error("the root over the floor must be stored")
	}
}
