package plan

import (
	"context"
	"encoding/json"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/testutil"
	"github.com/afterdarksys/oos/internal/worklimit"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManifestIntegrityAndDeepVerification(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "file")
	testutil.Write(t, src, 4096)
	q, err := OpenQuarantine(filepath.Join(home, "q"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	q.Hash = true
	dst, err := q.take(src, 4096, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	v, err := VerifyBatch(q.Dir, q.Batch, true)
	if err != nil || v.Version != 2 || len(v.Entries) != 1 || v.Entries[0].Status != "quarantined" {
		t.Fatalf("verify: %+v %v", v, err)
	}
	testutil.Write(t, dst, 4096)
	v, err = VerifyBatch(q.Dir, q.Batch, true)
	if err != nil || v.Entries[0].Status != "changed" {
		t.Fatalf("changed payload not detected: %+v %v", v, err)
	}
	m, err := readManifest(q.Dir, q.Batch)
	if err != nil {
		t.Fatal(err)
	}
	m.Entries[0].Bytes++
	b, _ := json.Marshal(m)
	os.WriteFile(q.manifestPath(), b, 0o600)
	if _, err := VerifyBatch(q.Dir, q.Batch, true); err == nil {
		t.Fatal("tampered manifest accepted")
	}
}
func TestRecoverIsMetadataOnlyAndDryByDefault(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "file")
	testutil.Write(t, src, 4096)
	q, err := OpenQuarantine(filepath.Join(home, "q"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identify(src)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(q.dest(src)), 0o700)
	q.man.Entries = []QEntry{{From: src, To: q.dest(src), Pending: true, Identity: id, Bytes: 4096}}
	if err = q.writeManifest(); err != nil {
		t.Fatal(err)
	}
	if _, err = RecoverBatch(q.Dir, q.Batch, false); err != nil {
		t.Fatal(err)
	}
	m, _ := readManifest(q.Dir, q.Batch)
	if len(m.Entries) != 1 {
		t.Fatal("dry recovery changed journal")
	}
	if _, err = RecoverBatch(q.Dir, q.Batch, true); err != nil {
		t.Fatal(err)
	}
	m, _ = readManifest(q.Dir, q.Batch)
	if len(m.Entries) != 0 {
		t.Fatal("proven source not reconciled")
	}
	if _, err = os.Stat(src); err != nil {
		t.Fatal("recovery touched payload", err)
	}
}
func TestRecoverPreservesAmbiguousLegacyRecord(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "file")
	testutil.Write(t, src, 4096)
	q, err := OpenQuarantine(filepath.Join(home, "q"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(q.dest(src)), 0o700)
	q.man.Entries = []QEntry{{From: src, To: q.dest(src), Pending: true}}
	if err = q.writeManifest(); err != nil {
		t.Fatal(err)
	}
	if _, err = RecoverBatch(q.Dir, q.Batch, true); err != nil {
		t.Fatal(err)
	}
	m, _ := readManifest(q.Dir, q.Batch)
	if len(m.Entries) != 1 {
		t.Fatal("ambiguous record discarded")
	}
}
func TestConfiguredQuarantineStores(t *testing.T) {
	home := t.TempDir()
	volume := filepath.Join(home, "volume")
	src := filepath.Join(volume, "cache", "file")
	testutil.Write(t, src, 4096)
	p := quarantinePolicy(home)
	p.StateFile = filepath.Join(home, "state.json")
	p.QuarantineVolumes = []config.QuarantineVolume{{Volume: volume, Directory: filepath.Join(volume, "q")}}
	cfg := &config.Config{Home: home, Policy: p}
	s, err := OpenStores(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer s.DiscardEmpty()
	q, err := s.For(src)
	if err != nil {
		t.Fatal(err)
	}
	if q.Dir != filepath.Join(volume, "q") {
		t.Fatal("wrong quarantine store")
	}
	if _, err = q.take(src, 4096, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(s.Batches()) != 1 {
		t.Fatal("batch missing from index")
	}
	if _, err = os.Stat(p.StateFile + ".quarantine-index.json"); err != nil {
		t.Fatal(err)
	}
	n, _, err := RestoreBatch(q.Dir, q.Batch, nil)
	if err != nil || n != 1 {
		t.Fatalf("restore %d: %v", n, err)
	}
}

func TestDeepVerificationBudgetPreservesPayload(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "payload")
	if err := os.WriteFile(src, []byte("data beyond budget"), 0600); err != nil {
		t.Fatal(err)
	}
	q, err := OpenQuarantine(filepath.Join(home, "store"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	q.Hash = true
	if _, err = q.take(src, 4096, time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx := worklimit.With(context.Background(), 100, 1)
	v, err := VerifyBatchContext(ctx, q.Dir, q.Batch, true)
	if err == nil && len(v.Entries) > 0 && v.Entries[0].Status == "quarantined" {
		t.Fatal("deep verification exceeded its read budget")
	}
	if _, err := os.Stat(q.dest(src)); err != nil {
		t.Fatal("verification changed payload", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := RestoreBatchContext(ctx, q.Dir, q.Batch, nil); err != context.Canceled {
		t.Fatalf("restore ignored cancellation: %v", err)
	}
	if _, err := os.Stat(q.dest(src)); err != nil {
		t.Fatal("cancelled restore changed payload", err)
	}
}
