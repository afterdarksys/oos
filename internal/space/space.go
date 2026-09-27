// Package space reports macOS disk use that a walk of the home directory
// cannot see: the purgeable estimate, leftover OS installers, Software
// Update downloads, a staged upgrade payload, and the process temporary
// directory. It never removes any of them.
package space

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/afterdarksys/oos/internal/size"
)

// Place is one path outside the home walk.
type Place struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	Kind  string `json:"kind"`
	Note  string `json:"note"`
	Error string `json:"error,omitempty"`
}

// Report is the sized-check section.
type Report struct {
	PurgeableBytes int64   `json:"purgeable_bytes,omitempty"`
	PurgeableNote  string  `json:"purgeable_note,omitempty"`
	PurgeableError string  `json:"purgeable_error,omitempty"`
	Places         []Place `json:"places,omitempty"`
}

// Cand is one path to measure. Glob is a filepath.Glob pattern.
type Cand struct {
	Path string
	Kind string
	Note string
	Glob bool
}

const (
	noteInstaller = "full macOS installer. Once this Mac is already running that OS, removing the app frees the space. oos does not remove it."
	noteUpdates   = "packages Software Update downloaded. A completed install usually deletes them; an interrupted one leaves them. oos does not remove them."
	notePayload   = "staged upgrade payload. Remove it only after that upgrade has finished booting; deleting it during an install breaks the install. oos does not remove it."
	noteTmp       = "process temporary directory. Live programs write here. oos does not remove it."
)

// DefaultCands are the places a home-directory walk misses on macOS.
func DefaultCands(tmpdir string) []Cand {
	c := []Cand{
		{Path: "/Library/Updates", Kind: "updates", Note: noteUpdates},
		{Path: "/macOS Install Data", Kind: "install-data", Note: notePayload},
		{Path: "/System/Volumes/Data/macOS Install Data", Kind: "install-data", Note: notePayload},
		{Path: "/Applications/Install macOS*.app", Kind: "installer", Note: noteInstaller, Glob: true},
	}
	if tmpdir != "" {
		c = append(c, Cand{Path: tmpdir, Kind: "tmp", Note: noteTmp})
	}
	return c
}

// Enabled gates the sized-check section. Tests turn it off so a check
// does not walk the machine's temporary directory or call osascript.
var Enabled = runtime.GOOS == "darwin"

// CandidateFn and PurgeableFn are the seams tests replace.
var (
	CandidateFn = DefaultCands
	PurgeableFn = Purgeable
)

// Collect asks for purgeable bytes and measures the known outside paths.
// It returns nil when the section is off (Linux, or a test).
func Collect(volume, tmpdir string) *Report {
	if !Enabled {
		return nil
	}
	r := &Report{}
	n, err := PurgeableFn(volume)
	if err != nil {
		r.PurgeableError = err.Error()
	} else {
		r.PurgeableBytes = n
		r.PurgeableNote = "CacheDelete estimate of bytes df's free number may include that an app still cannot use. A program that preallocates, or that asks for space which is not purgeable, returns \"No space left on device\" while df shows this gap as free. oos does not delete it."
	}
	r.Places = Measure(CandidateFn(tmpdir))
	return r
}

// Measure sizes each candidate. Missing paths and empty directories are
// skipped; a directory's size is its contents, not its own blocks. Two
// paths that are the same file (the install-data firmlink) are reported
// once. An unreadable path is reported with its error and no byte count.
func Measure(cands []Cand) []Place {
	var out []Place
	var seen []os.FileInfo
	add := func(p Place, fi os.FileInfo) {
		for _, prev := range seen {
			if os.SameFile(prev, fi) {
				return
			}
		}
		seen = append(seen, fi)
		out = append(out, p)
	}
	for _, c := range cands {
		paths := []string{c.Path}
		if c.Glob {
			m, err := filepath.Glob(c.Path)
			if err != nil || len(m) == 0 {
				continue
			}
			paths = m
		}
		for _, p := range paths {
			fi, err := os.Lstat(p)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				out = append(out, Place{Path: p, Kind: c.Kind, Note: c.Note, Error: err.Error()})
				continue
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				continue
			}
			var b int64
			var sizeErr error
			if fi.IsDir() {
				// The directory itself is never removed, and its own
				// blocks (4096 bytes on ext4, none on APFS) are not
				// space a report should point at. Count what is below it.
				b, sizeErr = size.PathSize(p)
				if own := size.Allocated(fi); b >= own {
					b -= own
				}
			} else {
				b = size.Allocated(fi)
			}
			if sizeErr != nil && b == 0 {
				add(Place{Path: p, Kind: c.Kind, Note: c.Note, Error: sizeErr.Error()}, fi)
				continue
			}
			if b == 0 {
				continue
			}
			add(Place{Path: p, Bytes: b, Kind: c.Kind, Note: c.Note}, fi)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

// Print is the sized-check section.
func Print(w io.Writer, r *Report) {
	if r == nil {
		return
	}
	fmt.Fprintln(w, "invisible space (reported only; oos removes none of it):")
	switch {
	case r.PurgeableError != "":
		fmt.Fprintf(w, "  purgeable  unavailable (%s)\n", r.PurgeableError)
	default:
		fmt.Fprintf(w, "  purgeable  %s  %s\n", size.Human(r.PurgeableBytes), r.PurgeableNote)
	}
	if len(r.Places) == 0 {
		fmt.Fprintln(w, "  no installer leftovers, Software Update downloads, or temporary files over nothing")
		return
	}
	for _, p := range r.Places {
		if p.Error != "" && p.Bytes == 0 {
			fmt.Fprintf(w, "  %9s  %-12s %s\n", "?", p.Kind, p.Path)
			fmt.Fprintf(w, "             %s (%s)\n", p.Note, p.Error)
			continue
		}
		fmt.Fprintf(w, "  %9s  %-12s %s\n", size.Human(p.Bytes), p.Kind, p.Path)
		fmt.Fprintf(w, "             %s\n", p.Note)
	}
}
