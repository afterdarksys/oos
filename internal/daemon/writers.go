package daemon

import (
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/afterdarksys/oos/internal/guard"
)

// Writer is an open file that grew between two samples, with its holder.
type Writer struct {
	Path    string `json:"path"`
	Command string `json:"command"`
	PID     int    `json:"pid"`
	Bytes   int64  `json:"bytes"`
	Delta   int64  `json:"delta"`
}

type sample struct {
	bytes int64
	pid   int
	cmd   string
}

// Sampler remembers the size of every open regular file at the last tick
// and names the ones that grew. Only files a process holds open count:
// that is exactly the set a runaway build, log or download is writing.
//
// A stat on a dead NFS or autofs mount can block forever, so stats run on a
// few workers under an overall budget. A path not stat'd in time is skipped
// and counted in Skipped; its previous size is carried forward. A path whose
// earlier stat is still stuck is not retried, and no new workers start while
// maxStuckStats stats are stuck, so a dead mount costs a bounded number of
// goroutines and never the tick.
type Sampler struct {
	prev map[string]sample
	At   time.Time

	Skipped int           // open files not stat'd during the last Sample
	Workers int           // concurrent stats; <= 0 means defaultStatWorkers
	Budget  time.Duration // overall stat deadline per Sample; <= 0 means defaultStatBudget

	lstat   func(string) (os.FileInfo, error)
	mu      sync.Mutex
	pending map[string]struct{} // paths whose stat has not returned
}

const (
	defaultStatWorkers = 8
	defaultStatBudget  = 5 * time.Second
	maxStuckStats      = 64
)

func NewSampler() *Sampler {
	return &Sampler{prev: map[string]sample{}, lstat: os.Lstat, pending: map[string]struct{}{}}
}

type statResult struct {
	f  guard.OpenFile
	fi os.FileInfo
	ok bool
}

// Sample takes the current open files, stats them, and returns the top n
// growers since the previous sample (none on the first call).
func (s *Sampler) Sample(files []guard.OpenFile, now time.Time, n int) []Writer {
	if s.prev == nil {
		s.prev = map[string]sample{}
	}
	if s.pending == nil {
		s.pending = map[string]struct{}{}
	}
	lstat := s.lstat
	if lstat == nil {
		lstat = os.Lstat
	}
	workers, budget := s.Workers, s.Budget
	if workers <= 0 {
		workers = defaultStatWorkers
	}
	if budget <= 0 {
		budget = defaultStatBudget
	}

	seen := map[string]bool{}
	var todo, skipped []guard.OpenFile
	s.mu.Lock()
	if room := maxStuckStats - len(s.pending); room < workers {
		workers = room
	}
	for _, f := range files {
		if seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		if _, stuck := s.pending[f.Path]; stuck || workers <= 0 {
			skipped = append(skipped, f)
			continue
		}
		todo = append(todo, f)
	}
	s.mu.Unlock()

	jobs := make(chan guard.OpenFile, len(todo))
	for _, f := range todo {
		jobs <- f
	}
	close(jobs)
	results := make(chan statResult, len(todo))
	var stop atomic.Bool
	if len(todo) < workers {
		workers = len(todo)
	}
	for i := 0; i < workers; i++ {
		go func() {
			for f := range jobs {
				if stop.Load() {
					continue
				}
				s.mu.Lock()
				s.pending[f.Path] = struct{}{}
				s.mu.Unlock()
				fi, err := lstat(f.Path)
				s.mu.Lock()
				delete(s.pending, f.Path)
				s.mu.Unlock()
				results <- statResult{f: f, fi: fi, ok: err == nil}
			}
		}()
	}

	cur := map[string]sample{}
	done := map[string]bool{}
	timer := time.NewTimer(budget)
	defer timer.Stop()
collect:
	for len(done) < len(todo) {
		select {
		case r := <-results:
			done[r.f.Path] = true
			if r.ok && r.fi.Mode().IsRegular() {
				cur[r.f.Path] = sample{bytes: r.fi.Size(), pid: r.f.PID, cmd: r.f.Command}
			}
		case <-timer.C:
			stop.Store(true)
			break collect
		}
	}
	for _, f := range todo {
		if !done[f.Path] {
			skipped = append(skipped, f)
		}
	}
	s.Skipped = len(skipped)
	grow := len(s.prev) > 0
	for _, f := range skipped {
		// size unknown this tick: keep the last one so the file neither
		// vanishes nor reappears later as brand-new growth
		if old, ok := s.prev[f.Path]; ok {
			cur[f.Path] = old
		}
	}

	var grew []Writer
	if grow {
		for p, c := range cur {
			if old, ok := s.prev[p]; ok && c.bytes > old.bytes {
				grew = append(grew, Writer{Path: p, Command: c.cmd, PID: c.pid, Bytes: c.bytes, Delta: c.bytes - old.bytes})
			} else if !ok && c.bytes > 0 {
				// new since last sample: everything it holds is growth
				grew = append(grew, Writer{Path: p, Command: c.cmd, PID: c.pid, Bytes: c.bytes, Delta: c.bytes})
			}
		}
	}
	s.prev = cur
	s.At = now
	sort.Slice(grew, func(i, j int) bool {
		if grew[i].Delta != grew[j].Delta {
			return grew[i].Delta > grew[j].Delta
		}
		return grew[i].Path < grew[j].Path
	})
	if n > 0 && len(grew) > n {
		grew = grew[:n]
	}
	return grew
}
