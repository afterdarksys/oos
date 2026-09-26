package size

import "context"

// UniqueProbeMin remains for source compatibility. First-block clone inference
// has been retired because partially modified clones invalidate that heuristic.
var UniqueProbeMin int64 = 128 << 10

// Unique reports allocated bytes and an upper estimate after external and
// in-set hardlinks are accounted for. Clone/snapshot ownership remains unknown.
func Unique(roots ...string) (allocated, unique int64, err error) {
	return UniqueAgainst(nil, roots...)
}
func UniqueAgainst(keep []string, roots ...string) (allocated, unique int64, err error) {
	a, err := Account(context.Background(), keep, roots...)
	return a.Allocated, a.Upper, err
}
