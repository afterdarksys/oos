package main

// Hierarchical size cache. A directory's size is remembered together with
// its mtime and the time it was measured. On the next walk, a directory
// whose mtime is unchanged and whose measurement is younger than the TTL is
// taken from the cache without descending. Entries are created and removed
// at their parent, which bumps the parent's mtime, so churny caches (uv
// archives, package caches) invalidate exactly where they changed. A file
// growing in place does not bump any directory mtime, so the TTL is the
// backstop for logs, databases and disk images; --fresh skips the cache.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type cacheEnt struct {
	Bytes int64     `json:"bytes"`
	Mtime int64     `json:"mtime_ns"`
	At    time.Time `json:"at"`
}

type sizeCache struct {
	mu      sync.Mutex
	path    string
	ttl     time.Duration
	entries map[string]cacheEnt
	dirty   bool
	hits    int
	misses  int
	enabled bool
}

var cache = &sizeCache{}

// openSizeCache loads the cache file; a missing or corrupt file starts empty.
func openSizeCache(path string, ttl time.Duration) *sizeCache {
	c := &sizeCache{path: path, ttl: ttl, entries: map[string]cacheEnt{}, enabled: path != ""}
	if !c.enabled {
		return c
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c.entries)
		if c.entries == nil {
			c.entries = map[string]cacheEnt{}
		}
	}
	return c
}

func (c *sizeCache) save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled || !c.dirty {
		return nil
	}
	// drop entries older than 4 TTLs so the file does not grow forever
	cutoff := time.Now().Add(-4 * c.ttl)
	for k, e := range c.entries {
		if e.At.Before(cutoff) {
			delete(c.entries, k)
		}
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(c.entries)
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	c.dirty = false
	return os.Rename(tmp, c.path)
}

func (c *sizeCache) get(dir string, mtime time.Time, now time.Time) (int64, bool) {
	if !c.enabled {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[dir]
	if !ok || e.Mtime != mtime.UnixNano() || now.Sub(e.At) > c.ttl {
		c.misses++
		return 0, false
	}
	c.hits++
	return e.Bytes, true
}

func (c *sizeCache) put(dir string, mtime time.Time, bytes int64, now time.Time) {
	if !c.enabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[dir] = cacheEnt{Bytes: bytes, Mtime: mtime.UnixNano(), At: now}
	c.dirty = true
}

func (c *sizeCache) stats() (hits, misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}

// sizeDir returns the allocated bytes under dir using the cache. Symlinks
// are counted as links and never followed; other devices are not crossed.
func (c *sizeCache) sizeDir(dir string, rootDev uint64, haveDev bool, now time.Time) int64 {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return 0
	}
	if haveDev {
		if dev, ok := deviceOf(fi); ok && dev != rootDev {
			return 0
		}
	}
	if b, ok := c.get(dir, fi.ModTime(), now); ok {
		return b
	}
	total := allocated(fi)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return total
		}
		return total
	}
	for _, de := range ents {
		p := filepath.Join(dir, de.Name())
		if de.IsDir() {
			total += c.sizeDir(p, rootDev, haveDev, now)
			continue
		}
		if cfi, err := os.Lstat(p); err == nil {
			total += allocated(cfi)
		}
	}
	c.put(dir, fi.ModTime(), total, now)
	return total
}

// pathSize returns allocated bytes under root, through the cache when one is
// open. The uncached walk lives in size.go as pathSizeWalk.
func pathSize(root string) (int64, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return allocated(info), nil
	}
	if !cache.enabled {
		return pathSizeWalk(root)
	}
	dev, have := deviceOf(info)
	return cache.sizeDir(root, dev, have, time.Now()), nil
}
