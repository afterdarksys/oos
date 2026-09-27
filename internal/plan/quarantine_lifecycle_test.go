package plan

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/testutil"
)

// A cache recreated at its original path must not pin the batch: only the
// quarantined object's presence and identity decide expiry. Restore still
// refuses to overwrite the recreated path.
func TestRecreatedSourceDoesNotHoldExpiry(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	now := time.Now()
	src := filepath.Join(home, "cache", "blob")
	testutil.Write(t, src, 4096)
	q, err := OpenQuarantine(dir, now.Add(-time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.take(src, 4096, now); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, src, 10) // the app recreates its cache
	bs, err := ListBatches(dir)
	if err != nil || len(bs) != 1 || bs[0].Count != 1 || bs[0].Held != "" {
		t.Fatalf("recreated source held the batch: %+v %v", bs, err)
	}
	n, skipped, err := RestoreBatch(dir, q.Batch, nil)
	if err != nil || n != 0 || len(skipped) != 1 {
		t.Fatalf("restore must refuse to overwrite: n=%d skipped=%v err=%v", n, skipped, err)
	}
	if b, _ := os.ReadFile(src); len(b) != 10 {
		t.Fatal("restore overwrote the recreated source")
	}
	_, names, err := PurgeBatches(dir, 0, now, false, false)
	if err != nil || len(names) != 1 {
		t.Fatalf("expiry purge: %v %v", names, err)
	}
}

func TestChangedOrMissingQuarantinedObjectStillHolds(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	now := time.Now()
	src := filepath.Join(home, "blob")
	testutil.Write(t, src, 4096)
	q, err := OpenQuarantine(dir, now.Add(-time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := q.take(src, 4096, now)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, dst, 1)
	if bs, _ := ListBatches(dir); len(bs) != 1 || bs[0].Count != -1 || !strings.Contains(bs[0].Held, "differs") {
		t.Fatalf("changed object not held: %+v", bs)
	}
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	if bs, _ := ListBatches(dir); len(bs) != 1 || bs[0].Count != -1 || !strings.Contains(bs[0].Held, "missing") {
		t.Fatalf("missing object not held: %+v", bs)
	}
	if _, names, err := PurgeBatches(dir, 0, now, true, false); err != nil || len(names) != 0 {
		t.Fatalf("held batch purged without includeHeld: %v %v", names, err)
	}
}

// An aborted take (cancellation, or a source that changed after the intent
// was journaled) moved nothing, so it must not leave a pending entry that
// pins the batch forever.
func TestAbortedTakeRollsBackIntent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		abort func(src string, cancel context.CancelFunc)
	}{
		{"cancelled", func(_ string, cancel context.CancelFunc) { cancel() }},
		{"source-changed", func(src string, _ context.CancelFunc) {
			if err := os.WriteFile(src, []byte("changed"), 0o600); err != nil {
				panic(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, "q")
			src := filepath.Join(home, "blob")
			testutil.Write(t, src, 4096)
			q, err := OpenQuarantine(dir, time.Now(), nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			q.Ctx = ctx
			q.checkpoint = func(stage string) error {
				if stage == "intent-durable" {
					tc.abort(src, cancel)
				}
				return nil
			}
			if _, err := q.take(src, 4096, time.Now()); err == nil {
				t.Fatal("aborted take succeeded")
			}
			if _, err := os.Stat(src); err != nil {
				t.Fatal("source lost", err)
			}
			m, err := readManifest(dir, q.Batch)
			if err != nil || len(m.Entries) != 0 {
				t.Fatalf("intent left journaled: %+v %v", m, err)
			}
			if bs, _ := ListBatches(dir); len(bs) != 1 || bs[0].Count != 0 {
				t.Fatalf("aborted take pinned the batch: %+v", bs)
			}
			if err := q.Discard(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A crash inside writeManifest leaves a .manifest-* temp. It is journal
// scaffolding: it must not hold the batch or block restore's cleanup.
func TestStaleManifestTempIsScaffolding(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	src := filepath.Join(home, "blob")
	testutil.Write(t, src, 4096)
	q, err := OpenQuarantine(dir, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.take(src, 4096, time.Now()); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(q.batchDir(), ".manifest-123456")
	if err := os.WriteFile(temp, []byte("{partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if bs, _ := ListBatches(dir); len(bs) != 1 || bs[0].Count != 1 {
		t.Fatalf("stale temp held the batch: %+v", bs)
	}
	if v, err := VerifyBatch(dir, q.Batch, false); err != nil || len(v.Issues) != 0 {
		t.Fatalf("stale temp reported as unrecorded: %+v %v", v, err)
	}
	n, _, err := RestoreBatch(dir, q.Batch, nil)
	if err != nil || n != 1 {
		t.Fatalf("restore: %d %v", n, err)
	}
	if _, err := os.Stat(q.batchDir()); !os.IsNotExist(err) {
		t.Fatal("empty batch with a stale temp not removed", err)
	}
	// Unrecorded data under a different name is still preserved.
	q2, err := OpenQuarantine(dir, time.Now().Add(time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(q2.batchDir(), "manifest-123")
	testutil.Write(t, other, 1)
	if err := q2.Discard(); err == nil {
		t.Fatal("unrecorded data discarded")
	}
}

// forgeEntry journals a quarantined object for from, as a damaged or
// tampered manifest would.
func forgeEntry(t *testing.T, q *Quarantine, from string) {
	t.Helper()
	to := q.dest(from)
	testutil.Write(t, to, 16)
	q.man.Entries = append(q.man.Entries, QEntry{From: from, To: to, Bytes: 16, At: time.Now()})
	if err := q.writeManifest(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRefusesProtectedTargets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	outside := t.TempDir()
	for _, tc := range []struct {
		name string
		from string
		run  func(dir, batch string) error
	}{
		{"os", "/etc/oos-restore-test", func(dir, batch string) error {
			_, _, err := RestoreBatch(dir, batch, nil)
			return err
		}},
		{"credentials", filepath.Join(home, ".ssh", "id_oos"), func(dir, batch string) error {
			_, _, err := RestoreBatch(dir, batch, nil)
			return err
		}},
		{"never_touch", filepath.Join(home, "keep", "x"), func(dir, batch string) error {
			p := config.Policy{AllowOutsideHome: true, NeverTouch: []string{filepath.Join(home, "keep")}}
			_, _, err := RestoreBatchPolicyContext(context.Background(), p, home, dir, batch, nil)
			return err
		}},
		{"outside-home", filepath.Join(outside, "sub", "x"), func(dir, batch string) error {
			_, _, err := RestoreBatchPolicyContext(context.Background(), config.Policy{}, home, dir, batch, nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(home, "q-"+tc.name)
			q, err := OpenQuarantine(dir, time.Now(), nil)
			if err != nil {
				t.Fatal(err)
			}
			legit := filepath.Join(home, "legit-"+tc.name, "f")
			forgeEntry(t, q, legit)
			forgeEntry(t, q, tc.from)
			before, err := os.ReadFile(q.manifestPath())
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.run(dir, q.Batch); err == nil {
				t.Fatalf("restore to %s accepted", tc.from)
			}
			if _, err := os.Lstat(tc.from); !os.IsNotExist(err) {
				t.Fatalf("protected target written: %v", err)
			}
			if _, err := os.Lstat(filepath.Dir(legit)); !os.IsNotExist(err) {
				t.Fatalf("restore wrote before refusing: %v", err)
			}
			if tc.name == "credentials" {
				if _, err := os.Lstat(filepath.Join(home, ".ssh")); !os.IsNotExist(err) {
					t.Fatal("~/.ssh created by a refused restore")
				}
			}
			after, _ := os.ReadFile(q.manifestPath())
			if !bytes.Equal(before, after) {
				t.Fatal("manifest rewritten by a refused restore")
			}
			if _, err := os.Stat(q.dest(tc.from)); err != nil {
				t.Fatal("payload moved by a refused restore", err)
			}
		})
	}
}

func TestRestoreRecreatesParentsPrivately(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	src := filepath.Join(home, "a", "b", "file")
	testutil.Write(t, src, 4096)
	q, err := OpenQuarantine(dir, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.take(src, 4096, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, "a")); err != nil {
		t.Fatal(err)
	}
	if n, _, err := RestoreBatch(dir, q.Batch, nil); err != nil || n != 1 {
		t.Fatalf("restore: %d %v", n, err)
	}
	for _, d := range []string{filepath.Join(home, "a"), filepath.Join(home, "a", "b")} {
		fi, err := os.Stat(d)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s recreated with %v: %v", d, fi.Mode().Perm(), err)
		}
	}
}

func TestRestoreRefusesForeignStore(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	src := filepath.Join(home, "blob")
	testutil.Write(t, src, 4096)
	q, err := OpenQuarantine(dir, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.take(src, 4096, time.Now()); err != nil {
		t.Fatal(err)
	}
	real := geteuid
	geteuid = func() int { return real() + 1 }
	defer func() { geteuid = real }()
	if _, _, err := RestoreBatch(dir, q.Batch, nil); err == nil || !strings.Contains(err.Error(), "owned by uid") {
		t.Fatalf("restore by another uid accepted: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("refused restore moved data")
	}
}

// A purge interrupted mid-delete leaves a tombstone, never a half batch that
// is still listed or restorable; the next purge finishes it.
func TestInterruptedPurgeLeavesTombstone(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	now := time.Now()
	q, err := OpenQuarantine(dir, now.Add(-time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		src := filepath.Join(home, "cache", name)
		testutil.Write(t, src, 4096)
		if _, err = q.take(src, 4096, now); err != nil {
			t.Fatal(err)
		}
	}
	tomb := filepath.Join(dir, tombstonePrefix+q.Batch)
	purgeCheckpoint = func(stage string) error {
		// Delete part of the batch, then crash.
		if err := os.Remove(filepath.Join(tomb, "manifest.json")); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(tomb, strings.TrimPrefix(filepath.Join(home, "cache", "a"), "/"))); err != nil {
			return err
		}
		return errors.New("injected crash mid-delete")
	}
	defer func() { purgeCheckpoint = nil }()
	if _, names, err := PurgeBatches(dir, 0, now, false, false); err == nil || len(names) != 0 {
		t.Fatalf("interrupted purge reported success: %v %v", names, err)
	}
	purgeCheckpoint = nil
	// Listed only as a stuck tombstone: held, never a restorable batch.
	if bs, err := ListBatches(dir); err != nil || len(bs) != 1 || !bs[0].Tombstone || bs[0].Count != -1 || !strings.HasPrefix(bs[0].Held, "purge incomplete: ") {
		t.Fatalf("half-deleted batch listed as a batch: %+v %v", bs, err)
	}
	if _, _, err := RestoreBatch(dir, q.Batch, nil); err == nil {
		t.Fatal("half-deleted batch restorable")
	}
	if _, err := os.Stat(tomb); err != nil {
		t.Fatal("tombstone missing", err)
	}
	_, names, err := PurgeBatches(dir, 0, now, false, false)
	if err != nil || len(names) != 1 || names[0] != q.Batch {
		t.Fatalf("next purge did not finish the tombstone: %v %v", names, err)
	}
	if _, err := os.Stat(tomb); !os.IsNotExist(err) {
		t.Fatal("tombstone survived", err)
	}
}

// The ensure dry-run must count only the batches it would purge, not every
// batch in the store.
func TestEnsureDryRunCountsOnlyExpiredBatches(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	p := quarantinePolicy(home)
	for i, created := range []time.Time{now.Add(-30 * 24 * time.Hour), now.Add(-time.Hour)} {
		src := filepath.Join(home, "cache", string(rune('a'+i)))
		testutil.Write(t, src, 4096)
		q, err := OpenQuarantine(p.QuarantineDir, created, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = q.take(src, 4096, now); err != nil {
			t.Fatal(err)
		}
	}
	du, err := size.Disk(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Home: home, Volume: home, Policy: p}
	res, err := Ensure(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, du.FreeGB()+100, nil, false, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range res.Steps {
		if strings.Contains(s, "expired quarantine batches") {
			found = true
			if !strings.Contains(s, "would permanently delete 1 expired") {
				t.Errorf("dry-run counted batches that are not expired: %q", s)
			}
		}
	}
	if !found {
		t.Fatalf("no purge step: %v", res.Steps)
	}
}
