package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/afterdarksys/oos/internal/plan"
)

// RecoveryBrake is the persisted auto-act state (.auto-act.json): the
// failure brake and the time of the last action, so the cooldown survives
// a restart.
type RecoveryBrake struct {
	Failures int       `json:"failures"`
	Paused   bool      `json:"paused"`
	Reason   string    `json:"reason,omitempty"`
	LastAct  time.Time `json:"last_act,omitempty"`
}

// lockBusy reports an ensure that never started because another oos held
// the mutation lock.
func lockBusy(err error) bool { return err != nil && errors.Is(err, syscall.EWOULDBLOCK) }

// notFailure reports an ensure error that says nothing about recovery: the
// lock was busy, or the daemon was stopped mid-run.
func notFailure(err error) bool { return lockBusy(err) || errors.Is(err, context.Canceled) }

func (b *RecoveryBrake) Observe(r plan.EnsureResult, err error, limit int) {
	if b.Paused || notFailure(err) {
		return
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
}
func (d *Daemon) saveRecovery() {
	cfg := d.config()
	if cfg.Policy.StateFile == "" {
		return
	}
	path := cfg.Policy.StateFile + ".auto-act.json"
	b, err := json.Marshal(d.recovery)
	if err == nil {
		err = plan.WriteRecord(path, b)
	}
	if err != nil {
		d.recovery.Paused = true
		d.recovery.Reason = fmt.Sprintf("cannot persist automatic cleanup state: %v", err)
	}
	d.set(func(s *Status) {
		s.AutoAct.Paused = d.recovery.Paused
		s.AutoAct.Reason = d.recovery.Reason
		s.AutoAct.Failures = d.recovery.Failures
	})
}
