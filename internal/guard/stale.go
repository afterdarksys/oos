package guard

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/afterdarksys/oos/internal/safefs"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/worklimit"
)

// ChildPlan is one direct child of an rm-stale-children directory.
type ChildPlan struct {
	Path    string      `json:"path"`
	Bytes   int64       `json:"bytes"`
	ModTime time.Time   `json:"mtime"`
	Info    os.FileInfo `json:"-"`              // identity captured for the execution recheck
	Keep    string      `json:"keep,omitempty"` // reason to keep; empty means delete
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

// Referenced folds for darwin: its /var, /tmp and /etc are links into
// /private, and its default volumes ignore case. Folding only adds matches.
var (
	refFoldPrivate = runtime.GOOS == "darwin"
	refFoldCase    = runtime.GOOS == "darwin"
)

// Referenced reports whether child appears in any reference as a whole path
// component: "/a/b" matches "/a/b", "/a/b/x", "... /a/b ..." and "x=/a/b,y",
// not "/a/bc". Both sides are compared in every form that can name the same
// file (cleaned, lsof-unescaped, and on darwin /private-folded and
// case-folded); a match in any form counts, so a doubtful reference keeps
// the path.
func Referenced(child string, refs []string) bool {
	if child == "" {
		return false
	}
	children := refForms(filepath.Clean(child))
	for _, r := range refs {
		for _, rf := range refForms(r) {
			for _, c := range children {
				if containsPath(rf, c) {
					return true
				}
			}
		}
	}
	return false
}

// refForms is s plus each alternative spelling of it.
func refForms(s string) []string {
	forms := []string{s}
	add := func(v string) {
		for _, f := range forms {
			if f == v {
				return
			}
		}
		forms = append(forms, v)
	}
	if strings.Contains(s, `\`) {
		add(decodeLsofName(s))
	}
	for _, f := range forms {
		if strings.HasPrefix(f, "/") {
			add(filepath.Clean(f))
		}
	}
	if refFoldPrivate {
		for _, f := range forms {
			add(unprivate(f))
		}
	}
	if refFoldCase {
		for _, f := range forms {
			add(strings.ToLower(f))
		}
	}
	return forms
}

// unprivate rewrites darwin's /private/var, /private/tmp and /private/etc to
// the short names wherever they end at a path boundary.
func unprivate(s string) string {
	if !strings.Contains(s, "/private/") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "/private/") {
			rest := s[i+len("/private"):]
			matched := false
			for _, a := range []string{"/var", "/tmp", "/etc"} {
				if strings.HasPrefix(rest, a) && (len(rest) == len(a) || refBoundary(rest[len(a)])) {
					matched = true
					break
				}
			}
			if matched {
				i += len("/private")
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// containsPath reports whether child occurs in r followed by a boundary.
func containsPath(r, child string) bool {
	off := 0
	for {
		i := strings.Index(r[off:], child)
		if i < 0 {
			return false
		}
		end := off + i + len(child)
		if end == len(r) || refBoundary(r[end]) {
			return true
		}
		off = off + i + 1
	}
}

// refBoundary is a byte that can end a path inside a command line or an
// lsof name.
func refBoundary(c byte) bool {
	switch c {
	case '/', ' ', '"', '\'', ':', '=', ',', ';', '\t', ')', ']', '}', '>', '|', '&', '`', '\n', '\r', 0:
		return true
	}
	return false
}

// ClassifyChildren decides, for each direct child of dir, whether it is kept
// or deleted. Kept: symlinks (never followed, never judged), lock files,
// anything a running process references, anything modified inside the stale
// floor. Everything else is a delete candidate. Every non-symlink child is
// sized so the plan can show what is kept and why.
func ClassifyChildren(dir string, staleAfter time.Duration, refs []string, now time.Time) ([]ChildPlan, error) {
	return ClassifyChildrenContext(context.Background(), dir, staleAfter, refs, now)
}
func ClassifyChildrenContext(ctx context.Context, dir string, staleAfter time.Duration, refs []string, now time.Time) ([]ChildPlan, error) {
	ents, err := safefs.ReadDir(dir)
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
		c := ChildPlan{Path: p, ModTime: fi.ModTime(), Info: fi}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			c.Keep = "symlink"
		case strings.HasPrefix(de.Name(), ".lock"):
			c.Keep = "lock file"
		case Referenced(p, refs):
			c.Keep = "referenced by a running process"
		default:
			newest, err := NewestChange(ctx, p)
			if err != nil {
				c.Keep = fmt.Sprintf("staleness unknown: %v", err)
				break
			}
			c.ModTime = newest
			if now.Sub(newest) < staleAfter {
				c.Keep = fmt.Sprintf("modified %s ago, inside the %s floor", now.Sub(newest).Round(time.Minute), staleAfter)
			}
		}
		if c.Keep != "symlink" {
			var err error
			c.Bytes, err = safefs.MeasureContext(ctx, p)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// StaleWalkMax bounds the entries NewestChange visits under one child. A
// child larger than this is kept: its age cannot be established cheaply.
var StaleWalkMax = 200000

// StaleUsesChangeTime counts ctime as well as mtime. Extracting an archive
// or copying with preserved times leaves old mtimes on new files; ctime is
// set by the kernel and records the arrival. It cannot be set from user
// space, so tests that age fixtures with Chtimes turn it off.
var StaleUsesChangeTime = true

// NewestChange is the newest modification (and, where the platform exposes
// it, status change) time of path and everything under it. Symlinks are not
// followed and a directory on another device is an error, as is exceeding
// StaleWalkMax or the context's work budget. Callers treat any error as
// "not stale".
func NewestChange(ctx context.Context, path string) (time.Time, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := os.Lstat(path)
	if err != nil {
		return time.Time{}, err
	}
	dev, ok := size.DeviceOf(root)
	if !ok {
		return time.Time{}, errors.New("device id unavailable")
	}
	var newest time.Time
	seen := 0
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := worklimit.Step(ctx); err != nil {
			return err
		}
		if seen++; seen > StaleWalkMax {
			return fmt.Errorf("more than %d entries under %s", StaleWalkMax, path)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.IsDir() {
			if dd, ok := size.DeviceOf(fi); !ok || dd != dev {
				return fmt.Errorf("mount boundary at %s", p)
			}
		}
		t := fi.ModTime()
		if StaleUsesChangeTime {
			if ct, ok := changeTime(fi); ok && ct.After(t) {
				t = ct
			}
		}
		if t.After(newest) {
			newest = t
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return newest, nil
}
