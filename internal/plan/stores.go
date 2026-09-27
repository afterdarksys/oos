package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/safefs"
	"github.com/afterdarksys/oos/internal/size"
	"os"
	"path/filepath"
	"time"
)

// Stores selects explicitly configured per-volume quarantine roots. No mount
// discovery creates directories, and cross-device moves never fall back to copy.
type Stores struct {
	cfg     *config.Config
	now     time.Time
	open    map[string]*Quarantine
	rolled  map[string][]*Quarantine // full batches, oldest first
	release func()                   // store locks taken in path order
}
type StoreBatch struct {
	Directory string `json:"directory"`
	Batch     string `json:"batch"`
}

func OpenStores(cfg *config.Config, now time.Time) (_ *Stores, err error) {
	s := &Stores{cfg: cfg, now: now, open: map[string]*Quarantine{}, rolled: map[string][]*Quarantine{}}
	if err := safefs.MkdirAll(cfg.Policy.QuarantineDir, 0o700); err != nil {
		return nil, err
	}
	// Lock every existing store up front in path order; stores on volumes
	// not yet used lock lazily (non-blocking, so never a deadlock).
	release, err := lockStores(cfg.Policy.QuarantineStores())
	if err != nil {
		return nil, err
	}
	s.release = release
	defer func() {
		if err != nil {
			_ = s.DiscardEmpty()
		}
	}()
	primary, err := OpenQuarantine(cfg.Policy.QuarantineDir, now, nil)
	if err != nil {
		return nil, err
	}
	primary.Hash = cfg.Policy.QuarantineHash
	s.open[cfg.Policy.QuarantineDir] = primary
	// The central index lists configured stores, not guessed removable volumes.
	index := struct {
		Version int      `json:"version"`
		Stores  []string `json:"stores"`
	}{1, cfg.Policy.QuarantineStores()}
	path := cfg.Policy.StateFile + ".quarantine-index.json"
	if cfg.Policy.StateFile != "" {
		if err = safefs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		b, _ := json.MarshalIndent(index, "", "  ")
		f, e := os.CreateTemp(filepath.Dir(path), ".quarantine-index-*")
		if e != nil {
			return nil, e
		}
		defer os.Remove(f.Name())
		if _, e = f.Write(b); e != nil {
			f.Close()
			return nil, e
		}
		if e = f.Sync(); e != nil {
			f.Close()
			return nil, e
		}
		if e = f.Close(); e != nil {
			return nil, e
		}
		if e = os.Rename(f.Name(), path); e != nil {
			return nil, e
		}
	}
	return s, nil
}
func (s *Stores) Primary() *Quarantine { return s.open[s.cfg.Policy.QuarantineDir] }

// For returns the open batch of path's store, rolling over to a new batch
// when the current one is full.
func (s *Stores) For(path string) (*Quarantine, error) {
	dir := s.cfg.Policy.QuarantineFor(path)
	if err := sameDevice(path, dir); err != nil {
		return nil, err
	}
	if q := s.open[dir]; q != nil && !q.full() {
		return q, nil
	}
	q, err := OpenQuarantine(dir, s.now, nil)
	if err != nil {
		return nil, err
	}
	q.Hash = s.cfg.Policy.QuarantineHash
	if old := s.open[dir]; old != nil {
		old.unlock() // the new batch holds the store lock now
		s.rolled[dir] = append(s.rolled[dir], old)
	}
	s.open[dir] = q
	return q, nil
}
func (s *Stores) Batches() []StoreBatch {
	out := []StoreBatch{}
	for _, dir := range s.cfg.Policy.QuarantineStores() {
		for _, q := range append(append([]*Quarantine{}, s.rolled[dir]...), s.open[dir]) {
			if q != nil && !q.Empty() {
				out = append(out, StoreBatch{dir, q.Batch})
			}
		}
	}
	return out
}

// DiscardEmpty removes empty batches and releases every store lock.
func (s *Stores) DiscardEmpty() error {
	defer s.unlock()
	var errs error
	for _, q := range s.open {
		errs = errors.Join(errs, q.Discard())
	}
	for _, qs := range s.rolled {
		for _, q := range qs {
			q.unlock()
		}
	}
	return errs
}

func (s *Stores) unlock() {
	if s.release != nil {
		s.release()
		s.release = nil
	}
}
func FindBatch(p config.Policy, name string) (string, error) {
	var found string
	for _, dir := range p.QuarantineStores() {
		if _, err := readManifest(dir, name); err == nil {
			if found != "" {
				return "", fmt.Errorf("batch %s exists in multiple stores; use --quarantine-store", name)
			}
			found = dir
		}
	}
	if found == "" {
		return "", fmt.Errorf("batch %s not found or manifest invalid", name)
	}
	return found, nil
}
func ConfiguredBytes(p config.Policy) int64 {
	var n int64
	for _, dir := range p.QuarantineStores() {
		n += QuarantineBytes(dir)
	}
	return n
}

// Keep size reporting available without opening/creating any quarantine store.
func StoreFilesystem(dir string) (size.Capabilities, error) { return size.Filesystem(dir) }

func ListStoreBatches(p config.Policy) ([]Batch, error) {
	return ListStoreBatchesContext(context.Background(), p)
}

func ListStoreBatchesContext(ctx context.Context, p config.Policy) ([]Batch, error) {
	var out []Batch
	for _, dir := range p.QuarantineStores() {
		bs, err := ListBatchesContext(ctx, dir)
		if err != nil {
			return out, err
		}
		for i := range bs {
			bs[i].Store = dir
		}
		out = append(out, bs...)
	}
	return out, nil
}

// PurgeStores purges every configured store. all ignores age; held batches
// (Count < 0) are only deleted when includeHeld is also set.
func PurgeStores(ctx context.Context, p config.Policy, age time.Duration, now time.Time, all, includeHeld bool) (int64, []string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var total int64
	var names []string
	var errs error
	for _, dir := range p.QuarantineStores() {
		n, ns, err := purgeBatchesContext(ctx, dir, age, now, all, includeHeld, nil)
		total += n
		for _, name := range ns {
			names = append(names, filepath.Join(dir, name))
		}
		errs = errors.Join(errs, err)
	}
	return total, names, partial(len(names) > 0, errs)
}
