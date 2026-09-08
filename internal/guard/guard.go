package guard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/afterdarksys/oos/internal/config"
)

// Env is what the guards need from the outside world. Tests inject it.
type Env struct {
	Home  string
	Procs func() ([]string, error) // running process command lines
	Cwds  func() ([]string, error) // running process working directories
	Open  func() ([]string, error) // every open file of every process; nil disables
}

func Real() (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, err
	}
	return Env{Home: filepath.Clean(home), Procs: ListProcesses, Cwds: ListProcessCwds, Open: ListOpenFiles}, nil
}

// ListProcesses returns every other process's command line, untruncated (-ww:
// without it macOS ps clips long lines and a path deep in an argument list is
// silently missed). Our own is dropped:
// an oos invocation names the paths it is judging, and must never count as
// a process that uses them.
func ListProcesses() ([]string, error) {
	out, err := exec.Command("ps", "-axww", "-o", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	self := strconv.Itoa(os.Getpid())
	var procs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		pid, cmd, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if pid == self {
			continue
		}
		procs = append(procs, strings.TrimSpace(cmd))
	}
	return procs, nil
}

// Refusal is a guard failure. The path is never touched when one is returned.
type Refusal struct {
	Rule string
	Msg  string
}

func (r *Refusal) Error() string { return r.Rule + ": " + r.Msg }

func Refuse(rule, f string, a ...any) error {
	return &Refusal{Rule: rule, Msg: fmt.Sprintf(f, a...)}
}

// CheckDeletable runs every guard for a destructive action on ent. Any doubt
// is a refusal. The order matters only for the message; all must pass.
func (e Env) CheckDeletable(p config.Policy, ent config.Entry) error {
	path := filepath.Clean(ent.Path)

	if ent.Action == config.ActionNever {
		return Refuse("action", "entry is marked never")
	}
	if ent.Action == config.ActionCommand {
		return nil // commands are guarded by AllowCommands in the executor, not by path rules
	}
	if !filepath.IsAbs(path) {
		return Refuse("absolute", "path %q is not absolute", path)
	}
	if path == string(filepath.Separator) {
		return Refuse("root", "refusing to operate on /")
	}
	if config.PathDepth(path) < p.MinPathDepth {
		return Refuse("depth", "%s has depth %d, policy requires >= %d", path, config.PathDepth(path), p.MinPathDepth)
	}
	if !p.AllowOutsideHome && !config.IsUnder(path, e.Home) {
		return Refuse("home", "%s is outside %s and allow_outside_home is false", path, e.Home)
	}
	for _, nt := range p.NeverTouch {
		if config.IsUnder(path, nt) {
			return Refuse("never_touch", "%s is under protected %s", path, nt)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Refuse("stat", "%v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Refuse("symlink", "%s is a symlink; oos does not follow links", path)
	}
	switch ent.Action {
	case config.ActionRmContents, config.ActionRmStaleChilds:
		if !info.IsDir() {
			return Refuse("kind", "%s is not a directory but action is %s", path, ent.Action)
		}
	case config.ActionRm:
		if !info.Mode().IsRegular() {
			return Refuse("kind", "%s is not a regular file but action is rm", path)
		}
	default:
		return Refuse("action", "unknown action %q", ent.Action)
	}
	if len(ent.GuardProcesses) > 0 {
		if e.Procs == nil {
			return Refuse("processes", "entry has guard_processes but no process lister is available")
		}
		procs, err := e.Procs()
		if err != nil {
			return Refuse("processes", "cannot list processes: %v", err)
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
				return Refuse("processes", "%d running process(es) match %q; stop them first", n, g)
			}
		}
	}
	return nil
}
