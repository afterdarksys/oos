package size

import "os"

// SyncAt avoids global writeback on the user's workstation. Directory fsync
// orders local metadata; free-space recovery can still lag or be retained.
func SyncAt(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
