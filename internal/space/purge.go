package space

import (
	"fmt"
	"strconv"
	"strings"
)

// ParsePurgeable reads two integers, available bytes then important-usage
// bytes, and returns the gap. A negative gap is reported as zero.
func ParsePurgeable(out string) (int64, error) {
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return 0, fmt.Errorf("purgeable: short reply %q", strings.TrimSpace(out))
	}
	avail, err1 := strconv.ParseInt(fields[0], 10, 64)
	important, err2 := strconv.ParseInt(fields[1], 10, 64)
	if err1 != nil || err2 != nil || avail < 0 || important < 0 {
		return 0, fmt.Errorf("purgeable: bad reply %q", strings.TrimSpace(out))
	}
	if important < avail {
		return 0, nil
	}
	return important - avail, nil
}
