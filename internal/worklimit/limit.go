// Package worklimit bounds cooperative filesystem work without global state.
package worklimit

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

// ErrExhausted is wrapped by every budget refusal; the message names the
// policy setting that raises the limit.
var ErrExhausted = errors.New("filesystem work budget exhausted")

type key struct{}
type limits struct {
	entries, bytes       atomic.Int64
	maxEntries, maxBytes int64
}

func With(ctx context.Context, entries, bytes int64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, key{}, &limits{maxEntries: entries, maxBytes: bytes})
}
func Step(ctx context.Context) error          { return consume(ctx, 1, false) }
func Read(ctx context.Context, n int64) error { return consume(ctx, n, true) }
func consume(ctx context.Context, n int64, read bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l, _ := ctx.Value(key{}).(*limits)
	if l == nil {
		return nil
	}
	c, max := &l.entries, l.maxEntries
	if read {
		c, max = &l.bytes, l.maxBytes
	}
	if max <= 0 {
		return nil
	}
	for {
		old := c.Load()
		if n < 0 || old > max-n {
			if read {
				return fmt.Errorf("%w: more than %d bytes to verify (raise policy.verification_max_bytes or narrow the entry)", ErrExhausted, max)
			}
			return fmt.Errorf("%w: more than %d filesystem entries (raise policy.scan_max_entries or narrow the entry)", ErrExhausted, max)
		}
		if c.CompareAndSwap(old, old+n) {
			return nil
		}
	}
}
