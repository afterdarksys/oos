package status

import (
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/size"
)

const (
	ExitOK       = 0
	ExitWarn     = 1
	ExitCritical = 2
	ExitUsage    = 3
)

// Of labels free space against the policy and picks the exit code.
func Of(p config.Policy, du size.DiskUsage) (string, int) {
	switch {
	case du.FreeGB() < p.MinFreeGB:
		return "CRITICAL", ExitCritical
	case du.FreeGB() < p.WarnFreeGB:
		return "WARN", ExitWarn
	}
	return "OK", ExitOK
}
