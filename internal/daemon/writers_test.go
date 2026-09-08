package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestSamplerNamesGrowers(t *testing.T) {
	home := t.TempDir()
	a, b, c := filepath.Join(home, "a.log"), filepath.Join(home, "b.bin"), filepath.Join(home, "c.tmp")
	testutil.Write(t, a, 100)
	testutil.Write(t, b, 100)
	files := []guard.OpenFile{{PID: 1, Command: "one", Path: a}, {PID: 2, Command: "two", Path: b}, {PID: 2, Command: "two", Path: b}, {PID: 9, Command: "x", Path: filepath.Join(home, "missing")}}
	s := NewSampler()
	now := time.Now()
	if got := s.Sample(files, now, 5); len(got) != 0 {
		t.Fatalf("first sample has no baseline: %+v", got)
	}
	testutil.Write(t, a, 5000) // grew
	testutil.Write(t, b, 50)   // shrank
	testutil.Write(t, c, 700)  // new since last sample
	files = append(files, guard.OpenFile{PID: 3, Command: "three", Path: c})
	got := s.Sample(files, now.Add(time.Minute), 5)
	if len(got) != 2 || got[0].Path != a || got[0].Delta != 4900 || got[0].Command != "one" || got[1].Path != c || got[1].Delta != 700 {
		t.Errorf("growers: %+v", got)
	}
	if got := s.Sample(files, now.Add(2*time.Minute), 1); len(got) != 0 {
		t.Errorf("nothing grew: %+v", got)
	}
	testutil.Write(t, a, 6000)
	testutil.Write(t, c, 9000)
	if got := s.Sample(files, now.Add(3*time.Minute), 1); len(got) != 1 || got[0].Path != c {
		t.Errorf("top-n keeps the biggest grower: %+v", got)
	}
}
