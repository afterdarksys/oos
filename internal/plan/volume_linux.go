package plan

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	btrfsMagic = 0x9123683e
	zfsMagic   = 0x2fc12fc1
)

// samePool compares statfs type and f_fsid, then, for btrfs and ZFS whose
// subvolumes/datasets differ in both, the backing device or pool from
// /proc/self/mountinfo.
func samePool(a, b string) bool {
	var sa, sb unix.Statfs_t
	if unix.Statfs(a, &sa) != nil || unix.Statfs(b, &sb) != nil || sa.Type != sb.Type {
		return false
	}
	if sa.Fsid == sb.Fsid && sa.Fsid != (unix.Fsid{}) {
		return true
	}
	if t := uint32(sa.Type); t != btrfsMagic && t != zfsMagic {
		return false
	}
	info, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	ma, oka := mountFor(string(info), a)
	mb, okb := mountFor(string(info), b)
	if !oka || !okb || ma.fstype != mb.fstype {
		return false
	}
	return poolOf(ma) == poolOf(mb)
}

type mountEntry struct{ point, fstype, source string }

// mountFor returns the mountinfo entry with the longest mount point that
// contains p. Nested btrfs subvolumes that are not mounted separately belong
// to the enclosing mount.
func mountFor(info, p string) (mountEntry, bool) {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	var best mountEntry
	found := false
	for _, line := range strings.Split(info, "\n") {
		left, right, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		lf, rf := strings.Fields(left), strings.Fields(right)
		if len(lf) < 5 || len(rf) < 2 {
			continue
		}
		point := unescapeMount(lf[4])
		if point != "/" && p != point && !strings.HasPrefix(p, point+"/") {
			continue
		}
		if !found || len(point) >= len(best.point) {
			best, found = mountEntry{point, rf[0], unescapeMount(rf[1])}, true
		}
	}
	return best, found
}

// poolOf is the btrfs device or the ZFS pool (the dataset's first element).
func poolOf(m mountEntry) string {
	if m.fstype == "zfs" {
		pool, _, _ := strings.Cut(m.source, "/")
		return pool
	}
	return m.source
}

func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			v := 0
			ok := true
			for _, c := range s[i+1 : i+4] {
				if c < '0' || c > '7' {
					ok = false
					break
				}
				v = v*8 + int(c-'0')
			}
			if ok {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
