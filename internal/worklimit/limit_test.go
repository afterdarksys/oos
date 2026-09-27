package worklimit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentBudget(t *testing.T) {
	ctx := With(context.Background(), 7, 10)
	var passed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if Step(ctx) == nil {
				passed.Add(1)
			}
		}()
	}
	wg.Wait()
	if passed.Load() != 7 {
		t.Fatalf("accepted %d entries", passed.Load())
	}
	if Read(ctx, 6) != nil || Read(ctx, 5) == nil || Read(ctx, 4) != nil {
		t.Fatal("byte accounting or rejected reservation changed budget")
	}
}
func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Step(With(ctx, 100, 100)) != context.Canceled {
		t.Fatal("cancellation not propagated")
	}
}

func TestExhaustionNamesSetting(t *testing.T) {
	ctx := With(context.Background(), 1, 1)
	_ = Step(ctx)
	err := Step(ctx)
	if !errors.Is(err, ErrExhausted) || !strings.Contains(err.Error(), "policy.scan_max_entries") {
		t.Fatalf("entry refusal not actionable: %v", err)
	}
	if err := Read(ctx, 2); !errors.Is(err, ErrExhausted) || !strings.Contains(err.Error(), "policy.verification_max_bytes") {
		t.Fatalf("byte refusal not actionable: %v", err)
	}
}
