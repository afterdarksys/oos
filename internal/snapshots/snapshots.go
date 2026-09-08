// Package snapshots reports Time Machine local snapshots on macOS. They are
// the space nothing else accounts for: a full disk with tens of GB inside
// APFS snapshots that no file walk can see. macOS does not report their size,
// so the report is a count and dates plus the exact command that thins them.
// oos never runs that command itself.
package snapshots

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	timeout = 15 * time.Second
	// Thin releases snapshots until this many bytes are free, at urgency 4
	// (the highest): the command the report prints.
	thinBytes = 10_000_000_000
)

// Snapshot is one local snapshot.
type Snapshot struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

// Report is what --check shows.
type Report struct {
	Count   int        `json:"count"`
	Oldest  time.Time  `json:"oldest,omitempty"`
	Newest  time.Time  `json:"newest,omitempty"`
	Items   []Snapshot `json:"snapshots,omitempty"`
	Thin    string     `json:"thin_command"`
	Note    string     `json:"note,omitempty"`
	Elapsed time.Duration
}

// Cmd runs tmutil; tests replace it.
var Cmd = func(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var stderr bytes.Buffer
	c := exec.CommandContext(ctx, "tmutil", args...)
	c.Stderr = &stderr
	out, err := c.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return out, nil
}

// Supported reports whether this platform has local snapshots to ask about.
func Supported() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath("tmutil")
	return err == nil
}

// Parse reads `tmutil listlocalsnapshots /` output: one
// com.apple.TimeMachine.YYYY-MM-DD-HHMMSS.local per line after a header.
func Parse(out []byte) []Snapshot {
	var snaps []Snapshot
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		const pre = "com.apple.TimeMachine."
		if !strings.HasPrefix(line, pre) {
			continue
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(line, pre), ".local")
		at, err := time.ParseInLocation("2006-01-02-150405", stamp, time.Local)
		if err != nil {
			continue
		}
		snaps = append(snaps, Snapshot{Name: line, At: at})
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].At.Before(snaps[j].At) })
	return snaps
}

// Collect asks tmutil for the local snapshots of volume.
func Collect(volume string) (*Report, error) {
	start := time.Now()
	out, err := Cmd("listlocalsnapshots", volume)
	if err != nil {
		return nil, fmt.Errorf("tmutil listlocalsnapshots: %v", err)
	}
	r := &Report{Items: Parse(out), Elapsed: time.Since(start)}
	r.Count = len(r.Items)
	if r.Count > 0 {
		r.Oldest, r.Newest = r.Items[0].At, r.Items[r.Count-1].At
	}
	r.Thin = fmt.Sprintf("tmutil thinlocalsnapshots %s %d 4", volume, thinBytes)
	r.Note = "macOS does not report the bytes a snapshot holds; the disk gets them back only when the snapshot goes"
	return r, nil
}

// Print is the --check section.
func Print(out io.Writer, r *Report, verbose bool) {
	if r.Count == 0 {
		fmt.Fprintln(out, "local snapshots: none")
		return
	}
	fmt.Fprintf(out, "local snapshots: %d (oldest %s, newest %s); %s\n", r.Count,
		r.Oldest.Format("2006-01-02 15:04"), r.Newest.Format("2006-01-02 15:04"), r.Note)
	fmt.Fprintf(out, "  release with: %s   (or: tmutil deletelocalsnapshots <date>)\n", r.Thin)
	if verbose {
		for _, s := range r.Items {
			fmt.Fprintf(out, "    %s  %s\n", s.At.Format("2006-01-02 15:04"), s.Name)
		}
	}
}
