package size

// Hierarchical size Active. A directory's size is remembered together with
// its mtime and the time it was measured. On the next walk, a directory
// whose mtime is unchanged and whose measurement is younger than the TTL is
// taken from the cache without descending. Entries are created and removed
// at their parent, which bumps the parent's mtime, so churny caches (uv
// archives, package caches) invalidate exactly where they changed. A file
// growing in place does not bump any directory mtime, so the TTL is the
// backstop for logs, databases and disk images; --fresh ignores every stored
// entry for one run and rewrites them from the measurements it makes, so the
// next run is warm again with honest numbers.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type cacheEnt struct {
	Bytes int64     `json:"bytes"`
	Mtime int64     `json:"mtime_ns"`
	At    time.Time `json:"at"`
}

// uniqEnt remembers one clone-aware measurement (see Unique) together with
// the allocated total it was measured against.
type uniqEnt struct {
	Allocated int64     `json:"allocated"`
	Unique    int64     `json:"unique"`
	At        time.Time `json:"at"`
}

type Cache struct {
	mu        sync.Mutex
	path      string
	ttl       time.Duration
	entries   map[string]cacheEnt
	dirty     bool
	uniq      map[string]uniqEnt // keyed by plan item; lives beside the size file
	uniqDirty bool
	hits      int
	misses    int
	enabled   bool
	Refresh   bool  // --fresh: never serve a stored entry, but store what is measured
	minBytes  int64 // directories smaller than this are not stored; their parent covers them
}

// cacheMinBytes keeps the cache file small: a hit on a large directory skips
// its whole subtree, so the small directories inside it never need entries.
const cacheMinBytes = 4 << 20

var Active = &Cache{}

// OpenCache loads the cache file; a missing or corrupt file starts empty.
func OpenCache(path string, ttl time.Duration) *Cache {
	return OpenCacheWith(path, ttl, cacheMinBytes)
}

// OpenCacheWith is openSizeCache with an explicit storage floor; tests use 0.
func OpenCacheWith(path string, ttl time.Duration, minBytes int64) *Cache {
	c := &Cache{path: path, ttl: ttl, entries: map[string]cacheEnt{}, uniq: map[string]uniqEnt{}, enabled: path != "", minBytes: minBytes}
	if !c.enabled {
		return c
	}
	if b, err := os.ReadFile(c.uniqPath()); err == nil {
		_ = json.Unmarshal(b, &c.uniq)
		if c.uniq == nil {
			c.uniq = map[string]uniqEnt{}
		}
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c.entries)
		if c.entries == nil {
			c.entries = map[string]cacheEnt{}
		}
		for k, e := range c.entries { // shrink a file written before the floor existed
			if e.Bytes < c.minBytes {
				delete(c.entries, k)
				c.dirty = true
			}
		}
	}
	return c
}

// uniqPath is the size file's sibling for clone-aware measurements, kept
// apart so an older oos reading sizes.json sees the shape it expects.
func (c *Cache) uniqPath() string { return strings.TrimSuffix(c.path, ".json") + ".unique.json" }

func (c *Cache) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled || (!c.dirty && !c.uniqDirty) {
		return nil
	}
	// drop entries older than 4 TTLs so the files do not grow forever
	cutoff := time.Now().Add(-4 * c.ttl)
	for k, e := range c.entries {
		if e.At.Before(cutoff) {
			delete(c.entries, k)
			c.dirty = true
		}
	}
	for k, e := range c.uniq {
		if e.At.Before(cutoff) {
			delete(c.uniq, k)
			c.uniqDirty = true
		}
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	if c.dirty {
		if err := writeJSON(c.path, c.entries); err != nil {
			return err
		}
		c.dirty = false
	}
	if c.uniqDirty {
		if err := writeJSON(c.uniqPath(), c.uniq); err != nil {
			return err
		}
		c.uniqDirty = false
	}
	return nil
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Unique serves the clone-aware total stored under key, or measures it with
// compute. A stored answer is served while it is younger than the TTL and
// was measured against the same allocated bytes: any change the size cache
// can see moves allocated, and the TTL covers the ones it cannot (a file
// rewritten in place). --fresh remeasures. A disabled cache measures every
// time. ok is false only when compute failed.
func (c *Cache) Unique(key string, allocated int64, now time.Time, compute func() (int64, error)) (int64, bool) {
	if c.enabled && !c.Refresh {
		c.mu.Lock()
		e, ok := c.uniq[key]
		c.mu.Unlock()
		if ok && e.Allocated == allocated && now.Sub(e.At) <= c.ttl {
			return e.Unique, true
		}
	}
	u, err := compute()
	if err != nil {
		return 0, false
	}
	if c.enabled {
		c.mu.Lock()
		c.uniq[key] = uniqEnt{Allocated: allocated, Unique: u, At: now}
		c.uniqDirty = true
		c.mu.Unlock()
	}
	return u, true
}

func (c *Cache) Get(dir string, mtime time.Time, now time.Time) (int64, bool) {
	if !c.enabled {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[dir]
	if c.Refresh || !ok || e.Mtime != mtime.UnixNano() || now.Sub(e.At) > c.ttl {
		c.misses++
		return 0, false
	}
	c.hits++
	return e.Bytes, true
}

func (c *Cache) Put(dir string, mtime time.Time, bytes int64, now time.Time) {
	if !c.enabled {
		return
	}
	if bytes < c.minBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[dir] = cacheEnt{Bytes: bytes, Mtime: mtime.UnixNano(), At: now}
	c.dirty = true
}

func (c *Cache) Stats() (hits, misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}

// SizeDir returns the allocated bytes under dir using the cache. Symlinks
// are counted as links and never followed; other devices are not crossed.
func (c *Cache) SizeDir(dir string, rootDev uint64, haveDev bool, now time.Time) int64 {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return 0
	}
	if haveDev {
		if dev, ok := DeviceOf(fi); ok && dev != rootDev {
			return 0
		}
	}
	if b, ok := c.Get(dir, fi.ModTime(), now); ok {
		return b
	}
	total := Allocated(fi)
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
			total += c.SizeDir(p, rootDev, haveDev, now)
			continue
		}
		if cfi, err := os.Lstat(p); err == nil {
			total += Allocated(cfi)
		}
	}
	c.Put(dir, fi.ModTime(), total, now)
	return total
}

// PathSize returns allocated bytes under root, through the cache when one is
// open. The uncached walk lives in size.go as pathSizeWalk.
func PathSize(root string) (int64, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return Allocated(info), nil
	}
	if !Active.enabled {
		return PathSizeWalk(root)
	}
	dev, have := DeviceOf(info)
	return Active.SizeDir(root, dev, have, time.Now()), nil
}
