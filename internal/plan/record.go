package plan

import (
	"github.com/afterdarksys/oos/internal/safefs"
	"os"
	"path/filepath"
)

// WriteRecord atomically persists an internal record; unlike payload moves,
// record replacement is intentional. Parents must not be symlink aliases.
func WriteRecord(path string, data []byte) error {
	if err := safefs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".oos-record-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
