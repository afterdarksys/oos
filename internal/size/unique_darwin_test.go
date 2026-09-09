package size

import (
	"path/filepath"
	"testing"

	"github.com/afterdarksys/oos/internal/testutil"
)

// TestPhysOffsetDistinctFilesDiffer: two unrelated files sit at two places
// on the device. The kernel answers through a struct packed to 4 bytes
// (sizeof 20, l2p_devoffset at 12); a Go struct laid out naturally reads
// the offset's upper half instead and files within the same 4 GB band all
// answer alike, which would fold most of a tree into one key.
func TestPhysOffsetDistinctFilesDiffer(t *testing.T) {
	home := t.TempDir()
	a := filepath.Join(home, "a")
	b := filepath.Join(home, "b")
	testutil.Write(t, a, 200<<10)
	testutil.Write(t, b, 200<<10)
	oa, oka := physOffset(a)
	ob, okb := physOffset(b)
	if !oka || !okb {
		t.Fatalf("probe failed: a=%v b=%v", oka, okb)
	}
	if oa == ob {
		t.Fatalf("distinct files answered the same offset %#x", oa)
	}
	if oa%4096 != 0 || ob%4096 != 0 {
		t.Errorf("offsets should be block aligned: %#x %#x", oa, ob)
	}
}
