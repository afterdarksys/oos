package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/afterdarksys/oos/internal/safefs"
)

const BatchLayout = "20060102-150405"

// QEntry is one quarantined path.
type QEntry struct {
	From    string    `json:"from"`
	To      string    `json:"to"`
	Bytes   int64     `json:"bytes"`
	At      time.Time `json:"at"`
	Pending bool      `json:"pending,omitempty"`
}

// Manifest is what a batch directory records about itself.
type Manifest struct {
	Batch   string    `json:"batch"`
	Created time.Time `json:"created"`
	Entries []QEntry  `json:"entries"`
}

// Quarantine is one run's batch. Paths are moved, never copied, so a take is
// instant and the space stays used until the batch is purged.
type Quarantine struct {
	Dir   string
	Batch string
	man   Manifest
	move  func(src, dst string) error
}

func OpenQuarantine(dir string, now time.Time, move func(src, dst string) error) (*Quarantine, error) {
	if move == nil {
		move = os.Rename
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := safefs.ReadDir(dir); err != nil {
		return nil, err
	}
	batch := now.Format(BatchLayout)
	if err := os.Mkdir(filepath.Join(dir, batch), 0o700); err != nil {
		if !os.IsExist(err) {
			return nil, err
		}
		unique, err := os.MkdirTemp(dir, batch+"-")
		if err != nil {
			return nil, err
		}
		batch = filepath.Base(unique)
	}
	q := &Quarantine{Dir: dir, Batch: batch, move: move}
	q.man = Manifest{Batch: batch, Created: now}
	return q, q.writeManifest()
}

func (q *Quarantine) batchDir() string     { return filepath.Join(q.Dir, q.Batch) }
func (q *Quarantine) manifestPath() string { return filepath.Join(q.batchDir(), "manifest.json") }

// dest mirrors the absolute source path under the batch directory.
func (q *Quarantine) dest(src string) string {
	return filepath.Join(q.batchDir(), strings.TrimPrefix(filepath.Clean(src), string(filepath.Separator)))
}

// take durably journals intent before moving. Pending entries are recoverable
// by restore but never automatically purged after an interrupted move.
func (q *Quarantine) take(src string, bytes int64, now time.Time) (string, error) {
	dst := q.dest(src)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	if err := safefs.CheckAncestors(src); err != nil {
		return "", err
	}
	if err := safefs.CheckAncestors(dst); err != nil {
		return "", err
	}
	if _, err := os.Lstat(dst); err == nil {
		return "", fmt.Errorf("quarantine destination %s already exists", dst)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	q.man.Entries = append(q.man.Entries, QEntry{From: src, To: dst, Bytes: bytes, At: now, Pending: true})
	if err := q.writeManifest(); err != nil {
		return "", err
	}
	if err := q.move(src, dst); err != nil {
		// Roll back only when the destination is absent and the source still exists.
		// An ambiguous/injected partial move remains journaled for recovery.
		_, srcErr := os.Lstat(src)
		_, dstErr := os.Lstat(dst)
		if srcErr == nil && os.IsNotExist(dstErr) {
			q.man.Entries = q.man.Entries[:len(q.man.Entries)-1]
			return "", errors.Join(err, q.writeManifest())
		}
		return "", err
	}
	if err := syncParents(filepath.Dir(src)); err != nil {
		return "", err
	}
	if err := syncParents(filepath.Dir(dst)); err != nil {
		return "", err
	}
	q.man.Entries[len(q.man.Entries)-1].Pending = false
	return dst, q.writeManifest()
}

// Empty reports whether nothing has been recorded into the batch.
func (q *Quarantine) Empty() bool { return len(q.man.Entries) == 0 }

// Discard removes the batch directory when nothing was recorded into it, so
// a run that only executed commands, or whose every move failed, leaves no
// empty batch behind. A batch holding entries is left exactly as it is.
func (q *Quarantine) Discard() error {
	if !q.Empty() {
		return nil
	}
	return q.removeEmptyBatch()
}

func (q *Quarantine) writeManifest() error {
	b, err := json.MarshalIndent(q.man, "", "  ")
	if err != nil {
		return err
	}
	if err := safefs.CheckAncestors(q.manifestPath()); err != nil {
		return err
	}
	f, err := os.CreateTemp(q.batchDir(), ".manifest-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, q.manifestPath()); err != nil {
		return err
	}
	return syncParents(q.batchDir())
}

// Batch is a summary of one quarantine batch on disk.
type Batch struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Count   int       `json:"count"`
	Bytes   int64     `json:"bytes"`
}

func readManifest(dir, name string) (*Manifest, error) {
	if _, err := batchTime(name); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name, "manifest.json")
	if err := safefs.CheckAncestors(path); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("batch %s manifest: %w", name, err)
	}
	return &m, nil
}

// ListBatches returns batches oldest first. A directory without a readable
// manifest is reported with Count -1 so it is visible but never auto-purged.
func ListBatches(dir string) ([]Batch, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Batch
	for _, de := range ents {
		if !de.IsDir() {
			continue
		}
		created, err := batchTime(de.Name())
		if err != nil {
			continue // not one of ours
		}
		b := Batch{Name: de.Name(), Created: created}
		if m, err := readManifest(dir, de.Name()); err == nil {
			b.Count = len(m.Entries)
			for _, e := range m.Entries {
				b.Bytes += e.Bytes
				if e.Pending {
					b.Count = -1
				}
			}
			if !m.Created.IsZero() {
				b.Created = m.Created
			}
			q := &Quarantine{Dir: dir, Batch: de.Name(), man: *m}
			if q.checkContents() != nil {
				b.Count = -1
			}
		} else {
			b.Count = -1
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}

func QuarantineBytes(dir string) int64 {
	var n int64
	bs, _ := ListBatches(dir)
	for _, b := range bs {
		if b.Bytes > 0 {
			n += b.Bytes
		}
	}
	return n
}

// RestoreBatch moves every entry back to where it came from. An entry whose
// original path now exists is left in quarantine and reported. When every
// entry is restored the batch directory is removed.
func RestoreBatch(dir, name string, move func(src, dst string) error) (restored int, skipped []string, err error) {
	if move == nil {
		move = os.Rename
	}
	m, err := readManifest(dir, name)
	if err != nil {
		return 0, nil, err
	}
	q := &Quarantine{Dir: dir, Batch: name, man: *m}
	var remaining []QEntry
	for _, e := range m.Entries {
		// Never accept paths outside this batch from a damaged manifest.
		if !filepath.IsAbs(e.From) || filepath.Clean(e.To) != q.dest(e.From) {
			return restored, skipped, fmt.Errorf("invalid quarantine entry %q -> %q", e.From, e.To)
		}
		if err := safefs.CheckAncestors(e.To); err != nil {
			return restored, skipped, err
		}
		_, toErr := os.Lstat(e.To)
		_, fromErr := os.Lstat(e.From)
		if os.IsNotExist(toErr) && fromErr == nil {
			// Intent not executed, or a restore completed before its manifest update.
			continue
		}
		if fromErr == nil || !os.IsNotExist(fromErr) {
			skipped = append(skipped, e.From+" (exists or cannot be inspected)")
			remaining = append(remaining, e)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(e.From), 0o755); err != nil {
			skipped = append(skipped, e.From+" ("+err.Error()+")")
			remaining = append(remaining, e)
			continue
		}
		if err := safefs.CheckAncestors(e.From); err != nil {
			return restored, skipped, err
		}
		if err := move(e.To, e.From); err != nil {
			skipped = append(skipped, e.From+" ("+err.Error()+")")
			remaining = append(remaining, e)
			continue
		}
		if err := syncParents(filepath.Dir(e.From)); err != nil {
			return restored, skipped, err
		}
		if err := syncParents(filepath.Dir(e.To)); err != nil {
			return restored, skipped, err
		}
		restored++
	}
	q.man.Entries = remaining
	if err := q.writeManifest(); err != nil {
		return restored, skipped, err
	}
	if len(remaining) == 0 {
		return restored, skipped, q.removeEmptyBatch()
	}
	return restored, skipped, nil
}

// PurgeBatches permanently removes batches. With all=false only batches
// older than olderThan go; batches without a manifest are never auto-purged.
func PurgeBatches(dir string, olderThan time.Duration, now time.Time, all bool) (freed int64, names []string, err error) {
	return purgeBatches(dir, olderThan, now, all, nil)
}

func purgeBatches(dir string, olderThan time.Duration, now time.Time, all bool, remaining *int64) (freed int64, names []string, err error) {
	bs, err := ListBatches(dir)
	if err != nil {
		return 0, nil, err
	}
	for _, b := range bs {
		if b.Count < 0 && !all {
			continue
		}
		if !all && now.Sub(b.Created) < olderThan {
			continue
		}
		p := filepath.Join(dir, b.Name)
		// Preflight the whole batch before deleting any of it.
		bytes, scanErr := safefs.Measure(p)
		if scanErr != nil {
			err = errors.Join(err, scanErr)
			continue
		}
		if remaining != nil && bytes > *remaining {
			continue
		}
		if _, rmErr := safefs.Remove(p, remaining); rmErr != nil {
			err = errors.Join(err, rmErr)
			continue
		}
		freed += b.Bytes
		names = append(names, b.Name)
	}
	return freed, names, err
}

// batchTime accepts legacy second-resolution names and new collision suffixes.
func batchTime(name string) (time.Time, error) {
	if filepath.Base(name) != name || len(name) < len(BatchLayout) {
		return time.Time{}, fmt.Errorf("invalid batch name %q", name)
	}
	if len(name) > len(BatchLayout) {
		suffix := strings.TrimPrefix(name[len(BatchLayout):], "-")
		if name[len(BatchLayout)] != '-' || suffix == "" {
			return time.Time{}, fmt.Errorf("invalid batch name %q", name)
		}
		for _, c := range suffix {
			if c < '0' || c > '9' {
				return time.Time{}, fmt.Errorf("invalid batch suffix")
			}
		}
	}
	return time.ParseInLocation(BatchLayout, name[:len(BatchLayout)], time.Local)
}

func syncParents(path string) error {
	for {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

// removeEmptyBatch only removes empty scaffolding. Unrecorded files, symlinks,
// and mount points survive; restore/discard must never recursively delete data.
func (q *Quarantine) removeEmptyBatch() error {
	if _, err := safefs.Measure(q.batchDir()); err != nil {
		return err
	}
	var dirs []string
	err := filepath.WalkDir(q.batchDir(), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		if p == q.manifestPath() && d.Type().IsRegular() {
			return nil
		}
		return fmt.Errorf("unrecorded quarantine data preserved at %s", p)
	})
	if err != nil {
		return err
	}
	// No recursive removals, even if a writer creates a file after the walk.
	for i := len(dirs) - 1; i > 0; i-- {
		if err := os.Remove(dirs[i]); err != nil {
			return err
		}
	}
	if err := os.Remove(q.manifestPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Remove(q.batchDir())
}

// checkContents prevents automatic expiry from deleting files absent from a
// manifest left by an older release or an interrupted journal update.
func (q *Quarantine) checkContents() error {
	for _, e := range q.man.Entries {
		if !filepath.IsAbs(e.From) || filepath.Clean(e.To) != q.dest(e.From) {
			return fmt.Errorf("invalid quarantine entry %q", e.To)
		}
	}
	return filepath.WalkDir(q.batchDir(), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == q.batchDir() {
			return nil
		}
		if path == q.manifestPath() && d.Type().IsRegular() {
			return nil
		}
		for _, e := range q.man.Entries {
			if path == e.To {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() && strings.HasPrefix(e.To, path+string(filepath.Separator)) {
				return nil
			}
		}
		if d.IsDir() {
			return nil
		} // empty mirroring directories contain no unrecorded data
		return fmt.Errorf("unrecorded quarantine data at %s", path)
	})
}
