package space

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afterdarksys/oos/internal/testutil"
)

func TestParsePurgeable(t *testing.T) {
	n, err := ParsePurgeable("100\n140\n")
	if err != nil || n != 40 {
		t.Fatalf("gap: %d %v", n, err)
	}
	n, err = ParsePurgeable("50 40")
	if err != nil || n != 0 {
		t.Fatalf("negative gap is zero: %d %v", n, err)
	}
	if _, err := ParsePurgeable("nope"); err == nil {
		t.Fatal("garbage must fail")
	}
}

func TestMeasureDedupesAndSkipsEmpty(t *testing.T) {
	root := t.TempDir()
	inst := filepath.Join(root, "Install macOS Test.app")
	testutil.Write(t, filepath.Join(inst, "Contents", "Info.plist"), 2<<20)
	upd := filepath.Join(root, "Updates")
	testutil.Write(t, filepath.Join(upd, "pkg"), 3<<20)
	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	// the same directory listed twice is one row
	link := filepath.Join(root, "Updates-alias")
	if err := os.Symlink(upd, link); err != nil {
		t.Fatal(err)
	}
	got := Measure([]Cand{
		{Path: filepath.Join(root, "Install macOS*.app"), Kind: "installer", Note: noteInstaller, Glob: true},
		{Path: upd, Kind: "updates", Note: noteUpdates},
		{Path: link, Kind: "updates", Note: noteUpdates},
		{Path: empty, Kind: "tmp", Note: noteTmp},
		{Path: filepath.Join(root, "missing"), Kind: "install-data", Note: notePayload},
	})
	if len(got) != 2 {
		t.Fatalf("rows: %+v", got)
	}
	if got[0].Kind != "updates" || got[0].Bytes < 3<<20 {
		t.Errorf("largest first: %+v", got[0])
	}
	if got[1].Kind != "installer" || !strings.Contains(got[1].Path, "Install macOS") {
		t.Errorf("installer: %+v", got[1])
	}
	var buf bytes.Buffer
	Print(&buf, &Report{PurgeableBytes: 40, PurgeableNote: "estimate", Places: got})
	if !strings.Contains(buf.String(), "reported only") || !strings.Contains(buf.String(), "oos does not remove them") {
		t.Errorf("print:\n%s", buf.String())
	}
}

func TestCollectHonoursEnabled(t *testing.T) {
	old := Enabled
	Enabled = false
	t.Cleanup(func() { Enabled = old })
	if Collect("/", "") != nil {
		t.Fatal("disabled collect must be nil")
	}
}
