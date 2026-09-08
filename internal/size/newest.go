package size

import (
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// NewestFile finds the most recently modified regular file under p, with a
// bounded walk so a huge tree answers in seconds rather than minutes.
func NewestFile(p string, maxEntries int) (string, time.Time) {
	var best string
	var bestT time.Time
	n := 0
	_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		n++
		if n > maxEntries {
			return filepath.SkipAll
		}
		if d.IsDir() && strings.Count(strings.TrimPrefix(q, p), string(filepath.Separator)) >= 4 {
			return filepath.SkipDir
		}
		if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() && fi.ModTime().After(bestT) {
			best, bestT = q, fi.ModTime()
		}
		return nil
	})
	return best, bestT
}
