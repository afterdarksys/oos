package plan

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/afterdarksys/oos/internal/reserve"
)

// ErrPartial is wrapped into the error of a run that processed some items but
// refused or failed others, or ran out of time part way.
var ErrPartial = errors.New("partial run: some items were processed, others were refused or failed")

// partial wraps err with ErrPartial when some work was done.
func partial(did bool, err error) error {
	if err == nil || !did || errors.Is(err, ErrPartial) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrPartial, err)
}

// auditLog writes the append-only audit log. A full disk must not stop oos
// from freeing space, so on ENOSPC it unlinks the space reserve and retries
// once. If that fails too, a permanent delete proceeds with the audit on
// stderr; a quarantine move, whose journal is safety-critical, is refused.
type auditLog struct {
	w         io.Writer
	reserve   string // reserve file path; "" disables
	stderr    io.Writer
	permanent bool
	degraded  bool
}

func (a *auditLog) errw() io.Writer {
	if a.stderr != nil {
		return a.stderr
	}
	return os.Stderr
}

// line writes one line (with its newline) and, with sync, flushes it.
func (a *auditLog) line(s string, sync bool) error {
	if a.w == nil {
		return nil
	}
	if a.degraded {
		fmt.Fprintf(a.errw(), "oos audit: %s\n", s)
		return nil
	}
	err := writeLine(a.w, s, sync)
	if err == nil || !errors.Is(err, syscall.ENOSPC) {
		return err
	}
	if released, _ := reserve.Release(a.reserve); released {
		if err = writeLine(a.w, s, sync); err == nil || !errors.Is(err, syscall.ENOSPC) {
			return err
		}
	}
	if !a.permanent {
		return fmt.Errorf("audit log: no space left on device; quarantine needs its journal and moves free nothing on the same volume. "+
			"Free space first with `oos --ensure N -y` (permanent deletes) or `oos --purge --yes`, or delete at least 64 MB by hand: %w", err)
	}
	a.degraded = true
	fmt.Fprintf(a.errw(), "oos: audit log unavailable: ENOSPC; audit to stderr (free at least 64 MB so the log can be written again)\n")
	fmt.Fprintf(a.errw(), "oos audit: %s\n", s)
	return nil
}

func writeLine(w io.Writer, s string, sync bool) error {
	if _, err := fmt.Fprintf(w, "%s\n", s); err != nil {
		return err
	}
	if f, ok := w.(interface{ Sync() error }); ok && sync {
		return f.Sync()
	}
	return nil
}
