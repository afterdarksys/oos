package guard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/afterdarksys/oos/internal/size"
)

// ChildPlan is one direct child of an rm-stale-children directory.
type ChildPlan struct {
	Path    string    `json:"path"`
	Bytes   int64     `json:"bytes"`
	ModTime time.Time `json:"mtime"`
	Keep    string    `json:"keep,omitempty"` // reason to keep; empty means delete
}

// References gathers every string a running process exposes that could name
// a path: full command lines, working directories and open files. Any failure is an
// error rather than a shorter list, so callers fail closed.
func (e Env) References() ([]string, error) {
	if e.Procs == nil {
		return nil, errors.New("no process lister available")
	}
	procs, err := e.Procs()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	refs := append([]string{}, procs...)
	if e.Cwds != nil {
		cwds, err := e.Cwds()
		if err != nil {
			return nil, fmt.Errorf("list process working directories: %w", err)
		}
		refs = append(refs, cwds...)
	}
	if e.Open != nil {
		open, err := e.Open()
		if err != nil {
			return nil, fmt.Errorf("list open files: %w", err)
		}
		refs = append(refs, open...)
	}
	return refs, nil
}

// Referenced reports whether child appears in any reference as a whole path
// component: "/a/b" matches "/a/b", "/a/b/x" and "... /a/b ...", not "/a/bc".
func Referenced(child string, refs []string) bool {
	for _, r := range refs {
		off := 0
		for {
			i := strings.Index(r[off:], child)
			if i < 0 {
				break
			}
			end := off + i + len(child)
			if end == len(r) {
				return true
			}
			switch r[end] {
			case '/', ' ', '"', '\'', ':', '=':
				return true
			}
			off = off + i + 1
		}
	}
	return false
}

// ClassifyChildren decides, for each direct child of dir, whether it is kept
// or deleted. Kept: symlinks (never followed, never judged), lock files,
// anything a running process references, anything modified inside the stale
// floor. Everything else is a delete candidate. Every non-symlink child is
// sized so the plan can show what is kept and why.
func ClassifyChildren(dir string, staleAfter time.Duration, refs []string, now time.Time) ([]ChildPlan, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]ChildPlan, 0, len(ents))
	for _, de := range ents {
		p := filepath.Join(dir, de.Name())
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		c := ChildPlan{Path: p, ModTime: fi.ModTime()}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			c.Keep = "symlink"
		case strings.HasPrefix(de.Name(), ".lock"):
			c.Keep = "lock file"
		case Referenced(p, refs):
			c.Keep = "referenced by a running process"
		case now.Sub(fi.ModTime()) < staleAfter:
			c.Keep = fmt.Sprintf("modified %s ago, inside the %s floor", now.Sub(fi.ModTime()).Round(time.Minute), staleAfter)
		}
		if c.Keep != "symlink" {
			c.Bytes, _ = size.PathSize(p)
		}
		out = append(out, c)
	}
	return out, nil
}
