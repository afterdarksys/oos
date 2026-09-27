package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/testutil"
	"golang.org/x/sys/unix"
)

// A batch holding an immutable file is held by purge instead of becoming a
// tombstone that fails every later purge.
func TestPurgeHoldsUndeletableBatch(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "q")
	src := filepath.Join(home, "blob")
	testutil.Write(t, src, 10)
	q, err := OpenQuarantine(dir, time.Now().Add(-time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := q.take(src, 10, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	q.Discard()
	if err := unix.Chflags(dst, unix.UF_IMMUTABLE); err != nil {
		t.Skip(err)
	}
	defer unix.Chflags(dst, 0)
	_, names, err := PurgeBatches(dir, 0, time.Now(), true, false)
	if len(names) != 0 || err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("undeletable batch purged or silently skipped: %v %v", names, err)
	}
	if _, err := os.Stat(filepath.Join(dir, q.Batch)); err != nil {
		t.Fatal("batch was tombstoned", err)
	}
	unix.Chflags(dst, 0)
	if _, names, err := PurgeBatches(dir, 0, time.Now(), true, false); err != nil || len(names) != 1 {
		t.Fatalf("purge after clearing the flag: %v %v", names, err)
	}
}
