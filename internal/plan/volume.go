package plan

import (
	"os"
	"path/filepath"
)

// sameVolume reports whether freeing space at p frees space on vol. It is
// sameDevice, except that btrfs subvolumes and ZFS datasets have their own
// st_dev (and f_fsid) while drawing on one pool; there the backing
// filesystem decides. It is only for space accounting: a rename still needs
// sameDevice.
func sameVolume(p, vol string) bool {
	pd, err1 := deviceOfPath(p)
	vd, err2 := deviceOfPath(vol)
	if err1 == nil && err2 == nil && pd == vd {
		return true
	}
	a, b := existingAncestor(p), existingAncestor(vol)
	if a == "" || b == "" {
		return false
	}
	return samePool(a, b)
}

func existingAncestor(p string) string {
	p = filepath.Clean(p)
	for {
		if _, err := os.Lstat(p); err == nil {
			return p
		} else if !os.IsNotExist(err) {
			return ""
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
}
