package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Env is what the guards need from the outside world. Tests inject it.
type Env struct {
	Home  string
	Procs func() ([]string, error) // running process command lines
	Cwds  func() ([]string, error) // running process working directories
}

func realEnv() (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, err
	}
	return Env{Home: filepath.Clean(home), Procs: listProcesses, Cwds: listProcessCwds}, nil
}

func listProcesses() ([]string, error) {
	out, err := exec.Command("ps", "-axo", "command=").Output()
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n"), nil
}

// isUnder reports whether p equals base or lives inside it. A base of "/"
// matches only "/" itself: listing the root in never_touch protects the root,
// it does not protect every path on the machine (the home and depth rules do
// that job with intent).
func isUnder(p, base string) bool {
	p = filepath.Clean(p)
	base = filepath.Clean(base)
	if p == base {
		return true
	}
	if base == string(filepath.Separator) {
		return false
	}
	return strings.HasPrefix(p, base+string(filepath.Separator))
}

func pathDepth(p string) int {
	p = filepath.Clean(p)
	if p == string(filepath.Separator) {
		return 0
	}
	return strings.Count(p, string(filepath.Separator))
}

// Refusal is a guard failure. The path is never touched when one is returned.
type Refusal struct {
	Rule string
	Msg  string
}

func (r *Refusal) Error() string { return r.Rule + ": " + r.Msg }

func refuse(rule, f string, a ...any) error {
	return &Refusal{Rule: rule, Msg: fmt.Sprintf(f, a...)}
}

// checkDeletable runs every guard for a destructive action on ent. Any doubt
// is a refusal. The order matters only for the message; all must pass.
func (e Env) checkDeletable(p Policy, ent Entry) error {
	path := filepath.Clean(ent.Path)

	if ent.Action == ActionNever {
		return refuse("action", "entry is marked never")
	}
	if ent.Action == ActionCommand {
		return nil // commands are guarded by AllowCommands in the executor, not by path rules
	}
	if !filepath.IsAbs(path) {
		return refuse("absolute", "path %q is not absolute", path)
	}
	if path == string(filepath.Separator) {
		return refuse("root", "refusing to operate on /")
	}
	if pathDepth(path) < p.MinPathDepth {
		return refuse("depth", "%s has depth %d, policy requires >= %d", path, pathDepth(path), p.MinPathDepth)
	}
	if !p.AllowOutsideHome && !isUnder(path, e.Home) {
		return refuse("home", "%s is outside %s and allow_outside_home is false", path, e.Home)
	}
	for _, nt := range p.NeverTouch {
		if isUnder(path, nt) {
			return refuse("never_touch", "%s is under protected %s", path, nt)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return refuse("stat", "%v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return refuse("symlink", "%s is a symlink; oos does not follow links", path)
	}
	switch ent.Action {
	case ActionRmContents, ActionRmStaleChilds:
		if !info.IsDir() {
			return refuse("kind", "%s is not a directory but action is %s", path, ent.Action)
		}
	case ActionRm:
		if !info.Mode().IsRegular() {
			return refuse("kind", "%s is not a regular file but action is rm", path)
		}
	default:
		return refuse("action", "unknown action %q", ent.Action)
	}
	if len(ent.GuardProcesses) > 0 {
		if e.Procs == nil {
			return refuse("processes", "entry has guard_processes but no process lister is available")
		}
		procs, err := e.Procs()
		if err != nil {
			return refuse("processes", "cannot list processes: %v", err)
		}
		for _, g := range ent.GuardProcesses {
			gl := strings.ToLower(g)
			n := 0
			for _, pr := range procs {
				if strings.Contains(strings.ToLower(pr), gl) {
					n++
				}
			}
			if n > 0 {
				return refuse("processes", "%d running process(es) match %q; stop them first", n, g)
			}
		}
	}
	return nil
}
