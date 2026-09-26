// Package trash measures and empties the operating system's own trash.
// On macOS that is ~/.Trash and each volume's .Trashes/<uid>. On Linux it
// is the freedesktop trash at ~/.local/share/Trash. Emptying deletes the
// contents permanently. It is not oos quarantine, and it never follows a
// symlink out of the bin.
package trash

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/size"
)

// Bin is one trash directory.
type Bin struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // home, volume, freedesktop
	Bytes   int64  `json:"bytes"`
	Entries int    `json:"entries"`
	Error   string `json:"error,omitempty"`
}

// VolumeRoot is where external volumes are mounted. Tests point it at a
// temporary directory.
var VolumeRoot = "/Volumes"

// Locate lists the trash bins for this user.
func Locate(home string) []Bin {
	return locate(home, os.Getuid())
}

func locate(home string, uid int) []Bin {
	var bins []Bin
	if runtime.GOOS == "darwin" {
		if b, ok := measure(filepath.Join(home, ".Trash"), "home"); ok {
			bins = append(bins, b)
		}
		bins = append(bins, volumes(uid, bins)...)
		return bins
	}
	if b, ok := measure(filepath.Join(home, ".local", "share", "Trash"), "freedesktop"); ok {
		bins = append(bins, b)
	}
	return bins
}

func volumes(uid int, have []Bin) []Bin {
	ents, err := os.ReadDir(VolumeRoot)
	if err != nil {
		return nil
	}
	var out []Bin
	for _, e := range ents {
		p, ok := volumePath(e.Name(), uid)
		if !ok {
			continue
		}
		b, ok := measure(p, "volume")
		if !ok {
			continue
		}
		if sameAs(b.Path, have) {
			continue
		}
		out = append(out, b)
		have = append(have, b)
	}
	return out
}

func volumePath(name string, uid int) (string, bool) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", false
	}
	if uid < 0 {
		return "", false
	}
	p := filepath.Join(VolumeRoot, name, ".Trashes", strconv.Itoa(uid))
	if filepath.Clean(p) != p {
		return "", false
	}
	return p, true
}

func measure(path, kind string) (Bin, bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return Bin{}, false
	}
	b := Bin{Path: path, Kind: kind}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		b.Error = "not a real directory; refusing"
		return b, true
	}
	b.Bytes, _ = size.PathSize(path)
	b.Entries = count(path, kind)
	return b, true
}

func count(path, kind string) int {
	if kind == "freedesktop" {
		return countDir(filepath.Join(path, "files")) + countDir(filepath.Join(path, "info"))
	}
	return countDir(path)
}

func countDir(path string) int {
	ents, err := os.ReadDir(path)
	if err != nil {
		return 0
	}
	return len(ents)
}

func sameAs(path string, bins []Bin) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	for _, b := range bins {
		other, err := os.Lstat(b.Path)
		if err == nil && os.SameFile(fi, other) {
			return true
		}
	}
	return false
}

// Allowed reports whether path is a trash bin this user may empty.
// The check is the shape of the path, not a promise that it exists.
func Allowed(path, home string, uid int) bool {
	path = filepath.Clean(path)
	if path == "" || path == string(filepath.Separator) || uid < 0 {
		return false
	}
	if home != "" {
		home = filepath.Clean(home)
		if path == filepath.Join(home, ".Trash") || path == filepath.Join(home, ".local", "share", "Trash") {
			return true
		}
	}
	rel, err := filepath.Rel(VolumeRoot, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 || parts[1] != ".Trashes" {
		return false
	}
	if parts[0] == "" || parts[0] == "." || parts[0] == ".." {
		return false
	}
	id, err := strconv.Atoi(parts[2])
	return err == nil && id == uid
}

// Empty deletes the contents of one bin. The bin directory itself stays.
// A symlink bin is refused. A symlink inside the bin is removed as a link.
// A child on another device is left in place.
func Empty(path, home string, uid int) (int64, int, error) {
	if !Allowed(path, home, uid) {
		return 0, 0, fmt.Errorf("refusing %s: not a trash bin", path)
	}
	path = filepath.Clean(path)
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return 0, 0, fmt.Errorf("refusing %s: not a real directory", path)
	}
	bytes, _ := size.PathSize(path)
	kind := "home"
	switch {
	case strings.HasSuffix(path, string(filepath.Separator)+filepath.Join(".local", "share", "Trash")):
		kind = "freedesktop"
	case strings.Contains(path, string(filepath.Separator)+".Trashes"+string(filepath.Separator)):
		kind = "volume"
	}
	roots := []string{path}
	if kind == "freedesktop" {
		roots = []string{filepath.Join(path, "files"), filepath.Join(path, "info")}
	}
	rootDev, have := size.DeviceOf(fi)
	n := 0
	for _, root := range roots {
		ents, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return bytes, n, err
		}
		for _, e := range ents {
			child := filepath.Join(root, e.Name())
			if !config.IsUnder(child, root) {
				continue
			}
			cfi, err := os.Lstat(child)
			if err != nil {
				return bytes, n, err
			}
			if cfi.IsDir() && cfi.Mode()&os.ModeSymlink == 0 && have {
				if dev, ok := size.DeviceOf(cfi); ok && dev != rootDev {
					continue
				}
			}
			if err := os.RemoveAll(child); err != nil {
				return bytes, n, err
			}
			n++
		}
	}
	return bytes, n, nil
}
