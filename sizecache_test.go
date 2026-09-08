package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSizeCacheHitsInvalidatesAndExpires(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "d")
	write(t, filepath.Join(dir, "sub", "a"), 65536)
	write(t, filepath.Join(dir, "b"), 65536)
	c := openSizeCache(filepath.Join(home, "sizes.json"), time.Hour)
	c.minBytes = 0 // test trees are tiny
	now := time.Now()
	fi, _ := os.Lstat(dir)
	dev, have := deviceOf(fi)

	n1 := c.sizeDir(dir, dev, have, now)
	if n1 < 2*65536 {
		t.Fatalf("size too small: %d", n1)
	}
	h, m := c.stats()
	if h != 0 || m == 0 {
		t.Fatalf("cold walk should miss: hits=%d misses=%d", h, m)
	}
	n2 := c.sizeDir(dir, dev, have, now.Add(time.Minute))
	h2, _ := c.stats()
	if n2 != n1 || h2 == 0 {
		t.Fatalf("warm walk should hit the root: n2=%d hits=%d", n2, h2)
	}

	// adding a child bumps the parent mtime and invalidates the root but not sub
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(dir, "c"), 65536)
	n3 := c.sizeDir(dir, dev, have, now.Add(2*time.Minute))
	if n3 <= n2 {
		t.Fatalf("new child not counted: %d <= %d", n3, n2)
	}

	// TTL expiry forces a recount
	before, _ := c.stats()
	c.sizeDir(dir, dev, have, now.Add(3*time.Hour))
	after, _ := c.stats()
	if after != before {
		t.Error("expired entries must not hit")
	}

	// save and reload round-trips
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	c2 := openSizeCache(filepath.Join(home, "sizes.json"), time.Hour)
	c2.minBytes = 0
	if b, ok := c2.get(dir, mustLstat(t, dir).ModTime(), now.Add(4*time.Minute)); !ok || b != n3 {
		t.Errorf("reloaded cache should serve the root: ok=%v b=%d want %d", ok, b, n3)
	}
	// disabled cache never hits
	off := openSizeCache("", time.Hour)
	if _, ok := off.get(dir, mustLstat(t, dir).ModTime(), now); ok {
		t.Error("disabled cache must not hit")
	}
}

func mustLstat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func TestPathSizeCachedMatchesWalk(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "d")
	write(t, filepath.Join(dir, "x", "y", "f"), 1<<20)
	write(t, filepath.Join(dir, "g"), 1<<19)
	if err := os.Symlink(home, filepath.Join(dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	walk, _ := pathSizeWalk(dir)
	old := cache
	cache = openSizeCache(filepath.Join(home, "sizes.json"), time.Hour)
	cache.minBytes = 0
	defer func() { cache = old }()
	cached, _ := pathSize(dir)
	if cached != walk {
		t.Errorf("cached %d != walk %d", cached, walk)
	}
	again, _ := pathSize(dir)
	if again != walk {
		t.Errorf("second cached read %d != walk %d", again, walk)
	}
}

func TestSizeCacheFloorSkipsSmallDirs(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "d")
	write(t, filepath.Join(dir, "small", "f"), 1024)
	write(t, filepath.Join(dir, "big", "f"), 8<<20)
	c := openSizeCache(filepath.Join(home, "sizes.json"), time.Hour)
	fi, _ := os.Lstat(dir)
	dev, have := deviceOf(fi)
	c.sizeDir(dir, dev, have, time.Now())
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
