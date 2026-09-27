package status

import (
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/size"
)

// Exit codes. 0-3 are the check-mode meanings (disk status, or a usage or
// config error); 4-6 say why a mutation did not finish, so monitoring can
// tell a retryable collision from a partial run from a broken disk.
const (
	ExitOK       = 0
	ExitWarn     = 1
	ExitCritical = 2
	ExitUsage    = 3
	ExitBusy     = 4 // another oos mutation holds the lock; nothing was tried; retry later
	ExitPartial  = 5 // some items were refused or failed, others may have been done
	ExitIO       = 6 // the audit log, quarantine or a record could not be opened or written
)

// Kind names an exit code for the "error_kind" field of JSON documents.
func Kind(code int) string {
	switch code {
	case ExitBusy:
		return "busy"
	case ExitPartial:
		return "partial"
	case ExitIO:
		return "io"
	case ExitUsage:
		return "usage"
	case ExitCritical:
		return "critical"
	}
	return ""
}

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
