package state

import (
	"os"
	"path/filepath"
	"sync"
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

func TestUnreadableStateIsUsableAndNeverOverwritten(t *testing.T) {
	// a directory where the file should be: ReadFile fails with something other than not-exist
	p := filepath.Join(t.TempDir(), "bigfile.json")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err == nil || s == nil || s.Known == nil {
		t.Fatalf("unreadable state: want usable state and an error, got %+v %v", s, err)
	}
	s.Record("check", size.DiskUsage{Free: 1 << 30, Total: 2 << 30}, time.Now())
	if err := Save(p, s); err == nil {
		t.Fatal("a state whose load failed must not be saved")
	}
	called := false
	got, err := Update(p, func(*State) { called = true })
	if err == nil || got == nil || called {
		t.Fatalf("update over an unreadable file: err=%v state=%v called=%v", err, got, called)
	}
	if fi, _ := os.Stat(p); fi == nil || !fi.IsDir() {
		t.Fatal("the unreadable path was replaced")
	}
}

func TestConcurrentUpdatesDoNotLoseWrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "st", "bigfile.json")
	const n = 40
	var wg sync.WaitGroup
	now := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := Update(p, func(s *State) {
				s.Record("t", size.DiskUsage{Free: 1 << 30, Total: 2 << 30}, now.Add(time.Duration(i)*time.Second))
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	s, err := Load(p)
	if err != nil || len(s.History) != n {
		t.Fatalf("want %d history points, got %d (%v)", n, len(s.History), err)
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".bigfile.json.tmp-*")); len(m) != 0 {
		t.Errorf("temp files left behind: %v", m)
	}
}
