package safefs

import "golang.org/x/sys/unix"

func renameExclusive(a int, src string, b int, dst string) error {
	return unix.RenameatxNp(a, src, b, dst, unix.RENAME_EXCL)
}
