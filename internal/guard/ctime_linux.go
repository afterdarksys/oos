package guard

import (
	"os"
	"syscall"
	"time"
)

// changeTime is the inode status-change time.
func changeTime(fi os.FileInfo) (time.Time, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(st.Ctim.Unix()), true
}
