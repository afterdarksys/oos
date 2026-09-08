package daemon

import (
	"os"
	"sort"
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
type Sampler struct {
	prev map[string]sample
	At   time.Time
}

func NewSampler() *Sampler { return &Sampler{prev: map[string]sample{}} }

// Sample takes the current open files, stats them, and returns the top n
// growers since the previous sample (none on the first call).
func (s *Sampler) Sample(files []guard.OpenFile, now time.Time, n int) []Writer {
	cur := map[string]sample{}
	for _, f := range files {
		if _, seen := cur[f.Path]; seen {
			continue
		}
		fi, err := os.Lstat(f.Path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		cur[f.Path] = sample{bytes: fi.Size(), pid: f.PID, cmd: f.Command}
	}
	var grew []Writer
	if len(s.prev) > 0 {
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
