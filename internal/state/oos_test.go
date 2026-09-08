package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/size"
)

func TestStateRoundTripAndCorruptRecovery(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "st", "bigfile.json")
	s := &State{Known: map[string]int64{"/a": 1}}
	s.Record("check", size.DiskUsage{Free: 1 << 30, Total: 2 << 30}, time.Now())
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got.Known["/a"] != 1 || len(got.History) != 1 {
		t.Fatalf("round trip failed: %v %+v", err, got)
	}
	_ = os.WriteFile(p, []byte("{not json"), 0o644)
	got, err = Load(p)
	if err != nil || got == nil {
		t.Fatal("corrupt state must yield a fresh state, not an error")
	}
	m, _ := filepath.Glob(p + ".corrupt-*")
	if len(m) != 1 {
		t.Error("corrupt state should be preserved under a .corrupt- name")
	}
}
