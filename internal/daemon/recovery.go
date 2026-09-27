package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/afterdarksys/oos/internal/mutation"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/safefs"
)

// RecoveryBrake is the persisted auto-act state (.auto-act.json): the
// failure brake, the time of the last action (so the cooldown survives a
// restart) and the time of the last scheduled purge (so it stays hourly).
type RecoveryBrake struct {
	Failures  int       `json:"failures"`
	Paused    bool      `json:"paused"`
	Reason    string    `json:"reason,omitempty"`
	LastAct   time.Time `json:"last_act,omitempty"`
	LastPurge time.Time `json:"last_purge,omitempty"`
}

// lockBusy reports an ensure that never started because another oos held
// the mutation lock. Only the lock's own sentinel counts: a bare EAGAIN (a
// failed fork, a full pipe) is a real failure, not a busy lock.
func lockBusy(err error) bool { return err != nil && errors.Is(err, mutation.ErrBusy) }

// partialRun reports an ensure that stopped part way (its time budget ran
// out, or some items were refused): what it did is judged by its result.
func partialRun(err error) bool {
	return err != nil && (errors.Is(err, plan.ErrPartial) || errors.Is(err, context.DeadlineExceeded))
}

// refusedRun reports an ensure the filesystem refused outright (it cannot
// move without replacing): a standing condition to report once, not a
// recovery failure to count toward the brake.
func refusedRun(err error) bool { return err != nil && errors.Is(err, safefs.ErrNoReplaceUnsupported) }

// notFailure reports an ensure error that says nothing about recovery: the
// lock was busy, the daemon was stopped mid-run, or the filesystem refused.
func notFailure(err error) bool {
	return lockBusy(err) || errors.Is(err, context.Canceled) || refusedRun(err)
}

func (b *RecoveryBrake) Observe(r plan.EnsureResult, err error, limit int) {
	if b.Paused || notFailure(err) {
		return
	}
	if partialRun(err) {
		err = nil // judged on what it recovered, like a complete run
	}
	if limit <= 0 {
		limit = 2
	}
	if err != nil || r.PoorRecovery {
		b.Failures++
	} else {
		b.Failures = 0
	}
	if b.Failures >= limit {
		b.Paused = true
		b.Reason = "automatic cleanup paused after repeated failures or poor space recovery; inspect quarantine and storage diagnostics"
	}
}

func (d *Daemon) loadRecovery() {
	p := d.cfg.Policy.StateFile + ".auto-act.json"
	if d.cfg.Policy.StateFile == "" {
		return
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return
	}
	if err != nil || json.Unmarshal(b, &d.recovery) != nil {
		d.recovery.Paused = true
		d.recovery.Reason = "automatic cleanup state unreadable; inspect before resuming"
	}
	d.st.AutoAct.LastAt = d.recovery.LastAct
	d.st.AutoAct.Paused = d.recovery.Paused
	d.st.AutoAct.Reason = d.recovery.Reason
	d.st.AutoAct.Failures = d.recovery.Failures
	d.st.LastPurge = d.recovery.LastPurge
}

// saveRecovery persists the brake. A failed write (a full disk is the usual
// cause, exactly when auto-act matters) does not pause anything: the state
// stays authoritative in memory, the error is reported, and the next tick
// retries the write.
func (d *Daemon) saveRecovery() error {
	cfg := d.config()
	if cfg.Policy.StateFile == "" {
		return nil
	}
	path := cfg.Policy.StateFile + ".auto-act.json"
	b, err := json.Marshal(d.recovery)
	if err == nil {
		err = plan.WriteRecord(path, b)
	}
	d.recoveryDirty = err != nil
	d.set(func(s *Status) {
		s.AutoAct.Paused = d.recovery.Paused
		s.AutoAct.Reason = d.recovery.Reason
		s.AutoAct.Failures = d.recovery.Failures
		s.AutoAct.PersistError = ""
		if err != nil {
			s.AutoAct.PersistError = err.Error()
		}
	})
	if err != nil {
		err = fmt.Errorf("cannot persist automatic cleanup state %q (kept in memory, retried next tick): %w", path, err)
		d.note(err.Error())
	}
	return err
}
