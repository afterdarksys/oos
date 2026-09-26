package size

import (
	"golang.org/x/sys/unix"
	"os"
)

func SyncAt(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Syncfs(int(f.Fd()))
}
