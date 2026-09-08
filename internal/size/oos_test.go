package size

import (
	"path/filepath"
	"testing"

	"github.com/afterdarksys/oos/internal/testutil"
)

func TestScanBigSortedAndCapped(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, filepath.Join(home, "s", "a"), 3<<20)
	testutil.Write(t, filepath.Join(home, "s", "b"), 5<<20)
	testutil.Write(t, filepath.Join(home, "s", "c"), 1<<20)
	testutil.Write(t, filepath.Join(home, "s", "tiny"), 10)
	hits, err := ScanBig(filepath.Join(home, "s"), 1<<20, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("want 2 hits, got %d", len(hits))
	}
	if filepath.Base(hits[0].Path) != "b" || filepath.Base(hits[1].Path) != "a" {
		t.Errorf("wrong order: %v", hits)
	}
}

// --- state and flags ---
