// Package appsafety says which application data is disposable and which
// holds something the user cannot get back. A browser cache regenerates.
// Bookmarks, cookies, passwords, mail and settings do not. This package
// only classifies; it never removes anything.
package appsafety

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/size"
)

// Class is the safety of a path.
type Class string

const (
	Disposable Class = "disposable"
	Keep       Class = "keep"
)

// Hit is one classified path found under an application directory.
type Hit struct {
	Path   string `json:"path"`
	Class  Class  `json:"class"`
	Reason string `json:"reason"`
	Bytes  int64  `json:"bytes"`
}

// Directory names that are caches wherever they sit inside an app's data.
// The whole directory is disposable; its contents are not inspected.
var disposableDir = map[string]string{
	"Cache":              "app cache; regenerates",
	"Caches":             "app cache; regenerates",
	"Code Cache":         "browser code cache; regenerates",
	"GPUCache":           "GPU cache; regenerates",
	"ShaderCache":        "shader cache; regenerates",
	"GrShaderCache":      "shader cache; regenerates",
	"cache2":             "Firefox disk cache; regenerates",
	"startupCache":       "Firefox startup cache; regenerates",
	"CacheStorage":       "service worker cache; regenerates",
	"Crashpad":           "crash dumps; safe to remove",
	"GPUPersistentCache": "GPU cache; regenerates",
}

// File names that are user data even when a parent directory looks like a cache.
var keepFile = map[string]string{
	"Bookmarks":             "bookmarks",
	"Bookmarks.bak":         "bookmarks",
	"Bookmarks.plist":       "bookmarks",
	"Cookies":               "cookies",
	"Cookies.binarycookies": "cookies",
	"cookies.sqlite":        "cookies",
	"Login Data":            "saved passwords",
	"Login Data-journal":    "saved passwords",
	"logins.json":           "saved passwords",
	"key4.db":               "password key database",
	"cert9.db":              "client certificates",
	"History":               "browsing history",
	"History-journal":       "browsing history",
	"places.sqlite":         "bookmarks and history",
	"places.sqlite-wal":     "bookmarks and history",
	"Web Data":              "autofill data",
	"Preferences":           "settings",
}

// Top-level ~/Library areas.
var disposableArea = map[string]string{
	"Caches":                  "cache; regenerates",
	"Logs":                    "logs; regenerates",
	"Saved Application State": "window restore state; regenerates",
	"WebKit":                  "WebKit cache; regenerates",
	"HTTPStorages":            "HTTP storage cache; regenerates",
}

var keepArea = map[string]string{
	"Application Support": "application data; bookmarks, cookies, passwords and mail live here",
	"Containers":          "app container; user data, not safe to remove as a whole",
	"Group Containers":    "shared app container; user data",
	"Preferences":         "settings",
	"LaunchAgents":        "login items",
	"Mail":                "mail",
	"Messages":            "messages",
	"Safari":              "Safari bookmarks and history",
	"Cookies":             "cookies",
}

// Area classifies one ~/Library area name. Unknown areas return "", "".
func Area(area string) (Class, string) {
	if reason, ok := disposableArea[area]; ok {
		return Disposable, reason
	}
	if reason, ok := keepArea[area]; ok {
		return Keep, reason
	}
	return "", ""
}

// Classify classifies path, which must sit under home/Library. A keep-file
// name wins over a disposable ancestor, so a Bookmarks file inside a Cache
// directory is still keep. Anything outside ~/Library is unknown.
func Classify(path, home string) (Class, string) {
	if home == "" || path == "" {
		return "", ""
	}
	lib := filepath.Join(home, "Library")
	if path != lib && !config.IsUnder(path, lib) {
		return "", ""
	}
	rel, err := filepath.Rel(lib, path)
	if err != nil || rel == "." {
		return "", ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	base := parts[len(parts)-1]
	if reason, ok := keepFile[base]; ok {
		return Keep, reason
	}
	for _, p := range parts {
		if reason, ok := disposableDir[p]; ok {
			return Disposable, reason
		}
	}
	if reason, ok := keepArea[parts[0]]; ok {
		return Keep, reason
	}
	if reason, ok := disposableArea[parts[0]]; ok {
		return Disposable, reason
	}
	return "", ""
}

const (
	maxDepth   = 8
	maxVisited = 20000
)

// Find walks root and returns disposable directories at or over minBytes,
// plus keep files it passed. A disposable directory is not descended into.
// Symlinks are skipped. This is a report; nothing is removed.
func Find(root string, minBytes int64) (safe, kept []Hit) {
	visited := 0
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > maxDepth || visited > maxVisited {
			return
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			visited++
			if visited > maxVisited {
				return
			}
			p := filepath.Join(dir, e.Name())
			fi, err := os.Lstat(p)
			if err != nil || fi.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if fi.IsDir() {
				if reason, ok := disposableDir[e.Name()]; ok {
					b, _ := size.PathSize(p)
					if b >= minBytes {
						safe = append(safe, Hit{Path: p, Class: Disposable, Reason: reason, Bytes: b})
					}
					continue
				}
				walk(p, depth+1)
				continue
			}
			if !fi.Mode().IsRegular() {
				continue
			}
			if reason, ok := keepFile[e.Name()]; ok {
				kept = append(kept, Hit{Path: p, Class: Keep, Reason: reason, Bytes: size.Allocated(fi)})
			}
		}
	}
	walk(root, 0)
	return safe, kept
}
