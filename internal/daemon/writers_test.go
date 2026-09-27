package daemon

import (
	"os"
	"path/filepath"
	"sync/atomic"
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

// A stat that never returns (dead NFS/autofs mount) must not stall the tick:
// it is skipped and counted, not retried while stuck, and the rest are sampled.
func TestSamplerBoundsHungStats(t *testing.T) {
	home := t.TempDir()
	a, hung := filepath.Join(home, "a.log"), filepath.Join(home, "nfs", "dead")
	testutil.Write(t, a, 100)
	release := make(chan struct{})
	var hungCalls atomic.Int32
	s := NewSampler()
	s.Budget = 100 * time.Millisecond
	s.lstat = func(p string) (os.FileInfo, error) {
		if p == hung {
			hungCalls.Add(1)
			<-release
			return nil, os.ErrNotExist
		}
		return os.Lstat(p)
	}
	files := []guard.OpenFile{{PID: 1, Command: "one", Path: a}, {PID: 2, Command: "nfs", Path: hung}}
	start := time.Now()
	s.Sample(files, start, 5)
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("sample blocked on a hung stat for %s", el)
	}
	if s.Skipped != 1 {
		t.Fatalf("hung path must be counted as skipped, got %d", s.Skipped)
	}
	testutil.Write(t, a, 900)
	got := s.Sample(files, start.Add(time.Minute), 5)
	if len(got) != 1 || got[0].Path != a || got[0].Delta != 800 {
		t.Errorf("other files must still be sampled: %+v", got)
	}
	if s.Skipped != 1 || hungCalls.Load() != 1 {
		t.Errorf("a stuck path must be skipped, not retried: skipped=%d calls=%d", s.Skipped, hungCalls.Load())
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.pending)
		s.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("released stat never cleared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.Sample(files, start.Add(2*time.Minute), 5)
	if s.Skipped != 0 || hungCalls.Load() != 2 {
		t.Errorf("after the mount recovers the path is stat'd again: skipped=%d calls=%d", s.Skipped, hungCalls.Load())
	}
}

// A skipped file keeps its last size, so it is not reported as new growth.
func TestSamplerCarriesSkippedSizes(t *testing.T) {
	home := t.TempDir()
	a := filepath.Join(home, "a.log")
	testutil.Write(t, a, 100)
	block := make(chan struct{})
	var blocking atomic.Bool
	s := NewSampler()
	s.Budget = 50 * time.Millisecond
	s.lstat = func(p string) (os.FileInfo, error) {
		if blocking.Load() {
			<-block
		}
		return os.Lstat(p)
	}
	files := []guard.OpenFile{{PID: 1, Command: "one", Path: a}}
	s.Sample(files, time.Now(), 5)
	blocking.Store(true)
	s.Sample(files, time.Now(), 5)
	blocking.Store(false)
	close(block)
	for {
		s.mu.Lock()
		n := len(s.pending)
		s.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := s.Sample(files, time.Now(), 5); len(got) != 0 {
		t.Errorf("a skipped file must not come back as new growth: %+v", got)
	}
}
