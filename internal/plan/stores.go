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
	cfg  *config.Config
	now  time.Time
	open map[string]*Quarantine
}
type StoreBatch struct {
	Directory string `json:"directory"`
	Batch     string `json:"batch"`
}

func OpenStores(cfg *config.Config, now time.Time) (*Stores, error) {
	s := &Stores{cfg: cfg, now: now, open: map[string]*Quarantine{}}
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
func (s *Stores) For(path string) (*Quarantine, error) {
	dir := s.cfg.Policy.QuarantineFor(path)
	if err := sameDevice(path, dir); err != nil {
		return nil, err
	}
	if q := s.open[dir]; q != nil {
		return q, nil
	}
	q, err := OpenQuarantine(dir, s.now, nil)
	if err != nil {
		return nil, err
	}
	q.Hash = s.cfg.Policy.QuarantineHash
	s.open[dir] = q
	return q, nil
}
func (s *Stores) Batches() []StoreBatch {
	out := []StoreBatch{}
	for _, dir := range s.cfg.Policy.QuarantineStores() {
		if q := s.open[dir]; q != nil && !q.Empty() {
			out = append(out, StoreBatch{dir, q.Batch})
		}
	}
	return out
}
func (s *Stores) DiscardEmpty() error {
	for _, q := range s.open {
		if err := q.Discard(); err != nil {
			return err
		}
	}
	return nil
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
	var out []Batch
	for _, dir := range p.QuarantineStores() {
		bs, err := ListBatches(dir)
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
	return total, names, errs
}
