package safefs

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func immutable(_ string, fi os.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	switch {
	case st.Flags&(unix.UF_IMMUTABLE|unix.SF_IMMUTABLE) != 0:
		return "immutable"
	case st.Flags&(unix.UF_APPEND|unix.SF_APPEND) != 0:
		return "append-only"
	}
	return ""
}
