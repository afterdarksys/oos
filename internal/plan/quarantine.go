package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/protect"
	"github.com/afterdarksys/oos/internal/safefs"
)

const BatchLayout = "20060102-150405"

// QEntry is one quarantined path.
type QEntry struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Bytes    int64     `json:"bytes"`
	At       time.Time `json:"at"`
	Pending  bool      `json:"pending,omitempty"`
	Identity *Identity `json:"identity,omitempty"`
	SHA256   string    `json:"sha256,omitempty"`
}

// Manifest is what a batch directory records about itself.
type Manifest struct {
	Version   int       `json:"version,omitempty"`
	Operation string    `json:"operation_id,omitempty"`
	Checksum  string    `json:"checksum,omitempty"`
	Batch     string    `json:"batch"`
	Created   time.Time `json:"created"`
	Entries   []QEntry  `json:"entries"`
}

// Quarantine is one run's batch. Paths are moved, never copied, so a take is
// instant and the space stays used until the batch is purged.
type Quarantine struct {
	Ctx        context.Context
	Dir        string
	Batch      string
	man        Manifest
	Hash       bool               // opt-in payload hashing
	checkpoint func(string) error // fault injection in package tests only
	move       func(src, dst string) error
}

func OpenQuarantine(dir string, now time.Time, move func(src, dst string) error) (*Quarantine, error) {
	if move == nil {
		move = safefs.MoveNoReplace
	}
	if err := safefs.MkdirAll(dir, 0o700); err != nil {
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
	id, err := operationID()
	if err != nil {
		return nil, err
	}
	q.man = Manifest{Version: 2, Operation: id, Batch: batch, Created: now}
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
	if q.Ctx == nil {
		q.Ctx = context.Background()
	}
	dst := q.dest(src)
	if err := safefs.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
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
	id, err := identify(src)
	if err != nil {
		return "", err
	}
	entry := QEntry{From: src, To: dst, Bytes: bytes, At: now, Pending: true, Identity: id}
	// abandon drops the intent only when nothing moved: the source is still in
	// place and the destination is absent. Anything else stays journaled.
	abandon := func(cause error) error {
		_, srcErr := os.Lstat(src)
		_, dstErr := os.Lstat(dst)
		if srcErr == nil && os.IsNotExist(dstErr) {
			q.man.Entries = q.man.Entries[:len(q.man.Entries)-1]
			return errors.Join(cause, q.writeManifest())
		}
		return cause
	}
	if q.Hash {
		entry.SHA256, err = treeDigestContext(q.Ctx, src)
		if err != nil {
			return "", err
		}
	}
	q.man.Entries = append(q.man.Entries, entry)
	if err := q.writeManifest(); err != nil {
		return "", err
	}
	if err := q.checkpointAt("intent-durable"); err != nil {
		return "", err
	}
	if err := q.Ctx.Err(); err != nil {
		return "", abandon(err)
	}
	if !id.matches(src) {
		return "", abandon(fmt.Errorf("source %s changed before move; nothing moved", src))
	}
	if err := q.move(src, dst); err != nil {
		// An ambiguous/injected partial move remains journaled for recovery.
		return "", abandon(err)
	}
	if err := q.checkpointAt("payload-moved"); err != nil {
		return "", err
	}
	if err := syncParents(filepath.Dir(src)); err != nil {
		return "", err
	}
	if err := syncParents(filepath.Dir(dst)); err != nil {
		return "", err
	}
	if !id.matches(dst) {
		return "", fmt.Errorf("moved object changed; pending journal preserved")
	}
	q.man.Entries[len(q.man.Entries)-1].Pending = false
	if err := q.checkpointAt("move-durable"); err != nil {
		return "", err
	}
	if err := q.writeManifest(); err != nil {
		return "", err
	}
	return dst, q.checkpointAt("complete-durable")
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
	if q.man.Version >= 2 {
		q.man.Checksum = manifestDigest(q.man)
	}
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
	Store   string    `json:"store,omitempty"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Count   int       `json:"count"`
	Bytes   int64     `json:"bytes"`
	Held    string    `json:"held,omitempty"` // why Count is -1
}

func readManifest(dir, name string) (*Manifest, error) {
	if _, err := batchTime(name); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name, "manifest.json")
	if err := safefs.CheckAncestors(path); err != nil {
		return nil, err
	}
	parent, err := safefs.OpenDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	file, err := parent.OpenFile("manifest.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("manifest is not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(file, 16<<20))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("batch %s manifest: %w", name, err)
	}
	if m.Version != 0 && m.Version != 2 {
		return nil, fmt.Errorf("unsupported manifest version %d", m.Version)
	}
	if m.Version == 2 && (m.Checksum == "" || m.Checksum != manifestDigest(m)) {
		return nil, fmt.Errorf("manifest checksum mismatch")
	}
	return &m, nil
}

// ListBatches returns batches oldest first. A batch that is not provably
// complete (no readable manifest, a pending entry, unrecorded data, or a
// quarantined object that is missing or changed) is reported with Count -1
// and the reason in Held, so it is visible but never auto-purged. A source
// path recreated after quarantine does not hold a batch: it only matters to
// restore, which refuses to overwrite it.
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
		hold := func(why string) {
			if b.Held == "" {
				b.Count, b.Held = -1, why
			}
		}
		if m, err := readManifest(dir, de.Name()); err == nil {
			b.Count = len(m.Entries)
			for _, e := range m.Entries {
				b.Bytes += e.Bytes
			}
			if !m.Created.IsZero() {
				b.Created = m.Created
			}
			q := &Quarantine{Dir: dir, Batch: de.Name(), man: *m}
			if err := q.checkContents(); err != nil {
				hold(err.Error())
			}
			for _, e := range m.Entries {
				if why := purgeBlocker(e); why != "" {
					hold(e.From + ": " + why)
				}
			}
		} else {
			hold("manifest: " + err.Error())
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
// entry is restored the batch directory is removed. Without a policy only the
// hard-coded always_disallowed list (resolved against $HOME) guards targets.
func RestoreBatch(dir, name string, move func(src, dst string) error) (int, []string, error) {
	return RestoreBatchContext(context.Background(), dir, name, move)
}
func RestoreBatchContext(ctx context.Context, dir, name string, move func(src, dst string) error) (restored int, skipped []string, err error) {
	home, _ := os.UserHomeDir()
	return RestoreBatchPolicyContext(ctx, config.Policy{AllowOutsideHome: true}, home, dir, name, move)
}

// geteuid is replaced in package tests only.
var geteuid = os.Geteuid

// RestoreBatchPolicyContext re-applies the removal-time path protections to
// every manifest target before anything is written: a forged or damaged
// manifest must not create files under the OS, credential directories,
// never_touch, or outside home. It refuses to run as a user other than the
// owner of the quarantine store.
func RestoreBatchPolicyContext(ctx context.Context, p config.Policy, home, dir, name string, move func(src, dst string) error) (restored int, skipped []string, err error) {
	if move == nil {
		move = safefs.MoveNoReplace
	}
	if err := checkStoreOwner(dir); err != nil {
		return 0, nil, err
	}
	m, err := readManifest(dir, name)
	if err != nil {
		return 0, nil, err
	}
	q := &Quarantine{Dir: dir, Batch: name, man: *m}
	for _, e := range m.Entries {
		// Never accept paths outside this batch from a damaged manifest.
		if !filepath.IsAbs(e.From) || filepath.Clean(e.To) != q.dest(e.From) {
			return 0, nil, fmt.Errorf("invalid quarantine entry %q -> %q", e.From, e.To)
		}
		if err := restoreTarget(p, home, e.From); err != nil {
			return 0, nil, err
		}
	}
	var remaining []QEntry
	for i, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return restored, skipped, err
		}
		if err := safefs.CheckAncestors(e.To); err != nil {
			return restored, skipped, err
		}
		_, toErr := os.Lstat(e.To)
		_, fromErr := os.Lstat(e.From)
		if os.IsNotExist(toErr) && fromErr == nil && e.Identity != nil && e.Identity.matches(e.From) {
			// Intent not executed, or a restore completed before its manifest update.
			continue
		}
		if fromErr == nil || !os.IsNotExist(fromErr) {
			skipped = append(skipped, e.From+" (exists or cannot be inspected)")
			remaining = append(remaining, e)
			continue
		}
		if err := safefs.MkdirAll(filepath.Dir(e.From), 0o700); err != nil {
			skipped = append(skipped, e.From+" ("+err.Error()+")")
			remaining = append(remaining, e)
			continue
		}
		// With the parents present, the full removal check also covers symlink
		// ancestors and physical aliases of protected paths.
		if err := guard.CheckRemovalPath(p, e.From, home); err != nil {
			return restored, skipped, err
		}
		if e.Identity != nil && !e.Identity.matches(e.To) {
			skipped = append(skipped, e.From+" (quarantined identity changed)")
			remaining = append(remaining, e)
			continue
		}
		if e.SHA256 != "" {
			hash, err := treeDigestContext(ctx, e.To)
			if err != nil || hash != e.SHA256 {
				skipped = append(skipped, e.From+" (content verification failed)")
				remaining = append(remaining, e)
				continue
			}
		}
		if err := ctx.Err(); err != nil {
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
		q.man.Entries = append(append([]QEntry{}, remaining...), m.Entries[i+1:]...)
		if err := q.writeManifest(); err != nil {
			return restored, skipped, err
		}
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

// checkStoreOwner refuses to act on a store owned by another user: restore
// would otherwise recreate that user's paths with this process's identity.
func checkStoreOwner(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("quarantine store %s: owner unavailable", dir)
	}
	if uid := geteuid(); uid < 0 || uint32(uid) != st.Uid {
		return fmt.Errorf("quarantine store %s is owned by uid %d; refusing to restore as uid %d", dir, st.Uid, uid)
	}
	return nil
}

// restoreTarget applies the removal-time protections that need no existing
// parent directory, then the full guard when the parent already exists.
func restoreTarget(p config.Policy, home, from string) error {
	from = filepath.Clean(from)
	if from == string(filepath.Separator) {
		return guard.Refuse("root", "refusing to restore onto /")
	}
	paths := []string{from, safefs.CanonicalAlias(from)}
	homes := []string{home, safefs.CanonicalAlias(home)}
	for _, path := range paths {
		for _, h := range homes {
			if prefix, ok := protect.Hit(path, h, p.AlwaysDisallowed); ok {
				return guard.Refuse("always_disallowed", "restore target %s is protected by %s", from, prefix)
			}
		}
		for _, nt := range p.NeverTouch {
			if config.IsUnder(path, nt) || config.IsUnder(path, safefs.CanonicalAlias(nt)) {
				return guard.Refuse("never_touch", "restore target %s is under protected %s", from, nt)
			}
		}
	}
	if !p.AllowOutsideHome && (home == "" || !(config.IsUnder(paths[0], homes[0]) || config.IsUnder(paths[1], homes[1]))) {
		return guard.Refuse("home", "restore target %s is outside %s and allow_outside_home is false", from, home)
	}
	if _, err := os.Lstat(filepath.Dir(from)); err == nil {
		return guard.CheckRemovalPath(p, from, home)
	}
	return nil
}

// tombstonePrefix marks a batch committed to deletion. ListBatches, FindBatch
// and restore do not recognise the name; the next purge finishes it.
const tombstonePrefix = ".purging-"

// purgeCheckpoint is fault injection in package tests only.
var purgeCheckpoint func(stage string) error

// PurgeBatches permanently removes batches. With all=false only batches
// older than olderThan go. Held batches (Count < 0) are skipped unless
// includeHeld is set.
func PurgeBatches(dir string, olderThan time.Duration, now time.Time, all, includeHeld bool) (freed int64, names []string, err error) {
	return purgeBatchesContext(context.Background(), dir, olderThan, now, all, includeHeld, nil)
}

func purgeBatches(dir string, olderThan time.Duration, now time.Time, all bool, remaining *int64) (freed int64, names []string, err error) {
	return purgeBatchesContext(context.Background(), dir, olderThan, now, all, false, remaining)
}

// finishTombstones deletes batches an earlier purge renamed but did not
// finish removing. They were already committed, so no budget applies.
func finishTombstones(ctx context.Context, dir string) (freed int64, names []string, err error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, err
	}
	for _, de := range ents {
		name, ok := strings.CutPrefix(de.Name(), tombstonePrefix)
		if !ok || !de.IsDir() {
			continue
		}
		if _, e := batchTime(name); e != nil {
			continue
		}
		n, e := safefs.RemoveContext(ctx, filepath.Join(dir, de.Name()), nil)
		if e != nil {
			err = errors.Join(err, e)
			continue
		}
		freed += n
		names = append(names, name)
	}
	return freed, names, err
}

func purgeBatchesContext(ctx context.Context, dir string, olderThan time.Duration, now time.Time, all, includeHeld bool, remaining *int64) (freed int64, names []string, err error) {
	freed, names, err = finishTombstones(ctx, dir)
	bs, listErr := ListBatches(dir)
	if listErr != nil {
		return freed, names, errors.Join(err, listErr)
	}
	for _, b := range bs {
		if b.Count < 0 && !includeHeld {
			continue
		}
		if !all && now.Sub(b.Created) < olderThan {
			continue
		}
		p := filepath.Join(dir, b.Name)
		// Preflight the whole batch before deleting any of it.
		bytes, scanErr := safefs.MeasureContext(ctx, p)
		if scanErr != nil {
			err = errors.Join(err, scanErr)
			continue
		}
		if remaining != nil && bytes > *remaining {
			continue
		}
		// Rename first so an interrupted delete never leaves a half batch that
		// is still listed or restorable.
		tomb := filepath.Join(dir, tombstonePrefix+b.Name)
		if mvErr := safefs.MoveNoReplace(p, tomb); mvErr != nil {
			err = errors.Join(err, mvErr)
			continue
		}
		if syncErr := syncParents(dir); syncErr != nil {
			err = errors.Join(err, syncErr)
		}
		if purgeCheckpoint != nil {
			if cpErr := purgeCheckpoint("tombstoned"); cpErr != nil {
				err = errors.Join(err, cpErr)
				continue
			}
		}
		if _, rmErr := safefs.RemoveContext(ctx, tomb, remaining); rmErr != nil {
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
	var dirs, temps []string
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
		if q.manifestTemp(p, d) {
			temps = append(temps, p)
			return nil
		}
		return fmt.Errorf("unrecorded quarantine data preserved at %s", p)
	})
	if err != nil {
		return err
	}
	for _, t := range temps {
		if err := os.Remove(t); err != nil && !os.IsNotExist(err) {
			return err
		}
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
	return q.checkContentsContext(context.Background())
}
func (q *Quarantine) checkContentsContext(ctx context.Context) error {
	for _, e := range q.man.Entries {
		if !filepath.IsAbs(e.From) || filepath.Clean(e.To) != q.dest(e.From) {
			return fmt.Errorf("invalid quarantine entry %q", e.To)
		}
	}
	return filepath.WalkDir(q.batchDir(), func(path string, d os.DirEntry, err error) error {
		if e := ctx.Err(); e != nil {
			return e
		}
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
		if q.manifestTemp(path, d) {
			return nil
		}
		return fmt.Errorf("unrecorded quarantine data at %s", path)
	})
}

// manifestTemp recognises a journal temp that writeManifest left behind when
// it was interrupted. It is scaffolding, not payload; payload is never a
// direct regular-file child of the batch named like one unless recorded,
// and recorded entries are matched before this is consulted.
func (q *Quarantine) manifestTemp(path string, d os.DirEntry) bool {
	return filepath.Dir(path) == q.batchDir() && strings.HasPrefix(d.Name(), ".manifest-") && d.Type().IsRegular()
}

func (q *Quarantine) checkpointAt(stage string) error {
	if q.checkpoint != nil {
		return q.checkpoint(stage)
	}
	return nil
}
