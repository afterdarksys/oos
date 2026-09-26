//go:build !darwin && !linux

package guard

import (
	"os"
	"time"
)

func changeTime(os.FileInfo) (time.Time, bool) { return time.Time{}, false }
