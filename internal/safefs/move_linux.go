package safefs

import "golang.org/x/sys/unix"

func renameExclusive(a int, src string, b int, dst string) error {
	return unix.Renameat2(a, src, b, dst, unix.RENAME_NOREPLACE)
}
