package daemon

import (
	"github.com/afterdarksys/oos/internal/plan"
	"testing"
)

func TestRecoveryBrake(t *testing.T) {
	var b RecoveryBrake
	b.Observe(plan.EnsureResult{PoorRecovery: true}, nil, 2)
	if b.Paused {
		t.Fatal("paused too soon")
	}
	b.Observe(plan.EnsureResult{}, nil, 2)
	if b.Failures != 0 {
		t.Fatal("success did not reset count")
	}
	b.Observe(plan.EnsureResult{PoorRecovery: true}, nil, 2)
	b.Observe(plan.EnsureResult{PoorRecovery: true}, nil, 2)
	if !b.Paused || b.Reason == "" {
		t.Fatal("poor recovery did not pause")
	}
	b.Observe(plan.EnsureResult{}, nil, 2)
	if !b.Paused {
		t.Fatal("pause automatically cleared")
	}
}
