package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const batchLayout = "20060102-150405"

// QEntry is one quarantined path.
type QEntry struct {
	From  string    `json:"from"`
	To    string    `json:"to"`
	Bytes int64     `json:"bytes"`
	At    time.Time `json:"at"`
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

func openQuarantine(dir string, now time.Time, move func(src, dst string) error) (*Quarantine, error) {
	if move == nil {
		move = os.Rename
	}
	batch := now.Format(batchLayout)
	if err := os.MkdirAll(filepath.Join(dir, batch), 0o755); err != nil {
		return nil, err
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

// take moves src into the batch and records it. The manifest is rewritten
// after every move so a crash mid-run leaves nothing unrecorded.
func (q *Quarantine) take(src string, bytes int64, now time.Time) (string, error) {
	dst := q.dest(src)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Lstat(dst); err == nil {
		return "", fmt.Errorf("quarantine destination %s already exists", dst)
	}
	if err := q.move(src, dst); err != nil {
		return "", err
	}
	q.man.Entries = append(q.man.Entries, QEntry{From: src, To: dst, Bytes: bytes, At: now})
	return dst, q.writeManifest()
}

func (q *Quarantine) writeManifest() error {
	b, err := json.MarshalIndent(q.man, "", "  ")
	if err != nil {
		return err
	}
	tmp := q.manifestPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, q.manifestPath())
}

// Batch is a summary of one quarantine batch on disk.
type Batch struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Count   int       `json:"count"`
	Bytes   int64     `json:"bytes"`
}

func readManifest(dir, name string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, name, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("batch %s manifest: %w", name, err)
	}
	return &m, nil
}

// listBatches returns batches oldest first. A directory without a readable
// manifest is reported with Count -1 so it is visible but never auto-purged.
func listBatches(dir string) ([]Batch, error) {
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
		created, err := time.ParseInLocation(batchLayout, de.Name(), time.Local)
		if err != nil {
			continue // not one of ours
		}
		b := Batch{Name: de.Name(), Created: created}
		if m, err := readManifest(dir, de.Name()); err == nil {
			b.Count = len(m.Entries)
			for _, e := range m.Entries {
				b.Bytes += e.Bytes
			}
			if !m.Created.IsZero() {
				b.Created = m.Created
			}
		} else {
			b.Count = -1
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}

func quarantineBytes(dir string) int64 {
	var n int64
	bs, _ := listBatches(dir)
	for _, b := range bs {
		if b.Bytes > 0 {
			n += b.Bytes
		}
	}
	return n
}

// restoreBatch moves every entry back to where it came from. An entry whose
// original path now exists is left in quarantine and reported. When every
// entry is restored the batch directory is removed.
func restoreBatch(dir, name string, move func(src, dst string) error) (restored int, skipped []string, err error) {
	if move == nil {
		move = os.Rename
	}
	m, err := readManifest(dir, name)
	if err != nil {
		return 0, nil, err
	}
	var remaining []QEntry
	for _, e := range m.Entries {
		if _, statErr := os.Lstat(e.From); statErr == nil {
			skipped = append(skipped, e.From+" (already exists)")
			remaining = append(remaining, e)
			continue
		}
		if mkErr := os.MkdirAll(filepath.Dir(e.From), 0o755); mkErr != nil {
			skipped = append(skipped, e.From+" ("+mkErr.Error()+")")
			remaining = append(remaining, e)
			continue
		}
		if mvErr := move(e.To, e.From); mvErr != nil {
			skipped = append(skipped, e.From+" ("+mvErr.Error()+")")
			remaining = append(remaining, e)
			continue
		}
		restored++
	}
	q := &Quarantine{Dir: dir, Batch: name, man: *m}
	q.man.Entries = remaining
	if len(remaining) == 0 {
		return restored, skipped, os.RemoveAll(q.batchDir())
	}
	return restored, skipped, q.writeManifest()
}

// purgeBatches permanently removes batches. With all=false only batches
// older than olderThan go; batches without a manifest are never auto-purged.
func purgeBatches(dir string, olderThan time.Duration, now time.Time, all bool) (freed int64, names []string, err error) {
	bs, err := listBatches(dir)
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
		makeWritable(p)
		if rmErr := os.RemoveAll(p); rmErr != nil {
			err = errors.Join(err, rmErr)
			continue
		}
		freed += b.Bytes
		names = append(names, b.Name)
	}
	return freed, names, err
}
