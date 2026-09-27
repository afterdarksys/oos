//go:build linux

package guard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// procRoot is where procfs is read from; tests point it at a fake tree.
var procRoot = "/proc"

// containerMarkers are files container runtimes create; any present means a
// container even when its PID 1 is a real init.
var containerMarkers = []string{"/.dockerenv", "/run/.containerenv"}

// hostInits are the PID 1 commands of a host's initial PID namespace. A
// container's PID 1 is usually a shell, tini, dumb-init or the service itself.
var hostInits = map[string]bool{"systemd": true, "init": true, "runit": true, "openrc-init": true, "launchd": true}

// procPIDs lists the numeric entries of procRoot. A missing /proc/self means
// procfs is not mounted (or not ours): an empty listing would read as
// "nothing is running", so that is an error.
func procPIDs() ([]int, error) {
	if _, err := os.Stat(filepath.Join(procRoot, "self")); err != nil {
		return nil, fmt.Errorf("%s is not mounted or not readable: %w", procRoot, err)
	}
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range ents {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// procGone reports an error that means the process (or the fd) went away.
func procGone(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func procDir(pid int) string { return filepath.Join(procRoot, strconv.Itoa(pid)) }

// procEuid and procOwner are seams for tests: our effective uid and the uid
// owning /proc/<pid> (world-readable, unlike its fd and cwd).
var (
	procEuid  = os.Geteuid
	procOwner = func(dir string) (int, error) {
		fi, err := os.Stat(dir)
		if err != nil {
			return 0, err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return 0, fmt.Errorf("no owner for %s", dir)
		}
		return int(st.Uid), nil
	}
)

// containerCgroups are cgroup path fragments container runtimes create.
var containerCgroups = []string{"/docker/", "/docker-", "containerd", "kubepods", "libpod", "/lxc/", "lxc.payload"}

func denied(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

// checkPIDNamespace returns ErrPIDNamespace unless this process can be shown
// to share the PID namespace of a host init. When PID 1's ns/pid link is
// readable it must equal ours. Non-root cannot read it (EACCES/EPERM); then
// the answer comes from the world-readable signals alone. In every case PID 1
// must be a known init, no container marker file may exist, and our cgroup
// must not be a container runtime's. Any other unreadable signal is a failure
// to verify, which is the same answer.
func checkPIDNamespace() error {
	self, err := os.Readlink(filepath.Join(procRoot, "self", "ns", "pid"))
	if err != nil {
		return fmt.Errorf("%w (own namespace unreadable: %v)", ErrPIDNamespace, err)
	}
	initNS, err := os.Readlink(filepath.Join(procDir(1), "ns", "pid"))
	switch {
	case err == nil:
		if self != initNS {
			return fmt.Errorf("%w (own %s, PID 1 %s)", ErrPIDNamespace, self, initNS)
		}
	case denied(err):
		// not ours to read; decide from the signals below
	default:
		return fmt.Errorf("%w (PID 1's namespace unreadable: %v)", ErrPIDNamespace, err)
	}
	comm, err := os.ReadFile(filepath.Join(procDir(1), "comm"))
	if err != nil {
		return fmt.Errorf("%w (PID 1's command unreadable: %v)", ErrPIDNamespace, err)
	}
	if c := strings.TrimSpace(string(comm)); !hostInits[c] {
		return fmt.Errorf("%w (PID 1 is %q, not a host init)", ErrPIDNamespace, c)
	}
	for _, m := range containerMarkers {
		if _, err := os.Lstat(m); err == nil {
			return fmt.Errorf("%w (%s exists)", ErrPIDNamespace, m)
		}
	}
	if cg, err := os.ReadFile(filepath.Join(procRoot, "self", "cgroup")); err == nil {
		for _, frag := range containerCgroups {
			if strings.Contains(string(cg), frag) {
				return fmt.Errorf("%w (cgroup names a container runtime: %s)", ErrPIDNamespace, frag)
			}
		}
	}
	return nil
}

// walkProcs calls fn for every process. An error from fn is ignored when the
// process has exited. When we are not root, a process owned by another uid
// whose entries are denied to us is not inspectable by design: it is counted
// (see OtherUserProcessesNotInspected) but does not fail the walk. Any other
// failure, including a denied process of our own uid or any unreadable
// process when root, counts as unreadable and ends the walk in an
// *UnreadableProcessesError. PID 1 must be visible: when it is not
// (hidepid), other users' processes are hidden and the listing is partial.
func walkProcs(what string, nsCheck bool, fn func(pid int, dir string) error) error {
	if nsCheck {
		if err := checkPIDNamespace(); err != nil {
			return err
		}
	}
	pids, err := procPIDs()
	if err != nil {
		return err
	}
	has1 := false
	for _, p := range pids {
		has1 = has1 || p == 1
	}
	if !has1 {
		return fmt.Errorf("process 1 is not visible in %s (mounted with hidepid?); other processes are hidden", procRoot)
	}
	euid := procEuid()
	bad, other := 0, 0
	for _, pid := range pids {
		dir := procDir(pid)
		if err := fn(pid, dir); err != nil {
			if procGone(err) {
				continue
			}
			owner, serr := procOwner(dir)
			if serr != nil && procGone(serr) {
				continue // exited between listing and reading
			}
			if serr == nil && euid != 0 && owner != euid && denied(err) {
				other++
				continue
			}
			bad++
		}
	}
	if nsCheck {
		otherUsersNotInspected.Store(int64(other))
	}
	if bad > 0 {
		return &UnreadableProcessesError{What: what, Count: bad, Seen: len(pids)}
	}
	return nil
}

// ListProcesses reads every other process's command line from
// /proc/<pid>/cmdline. Arguments are NUL-separated there, so spaces and
// newlines inside them survive; they are joined with spaces like ps shows
// them. A process with no command line (kernel thread, zombie) is listed as
// [comm]. No ps is needed: busybox ps rejects -x and slim images have none.
func ListProcesses() ([]string, error) {
	self := os.Getpid()
	var procs []string
	err := walkProcs("command lines", false, func(pid int, dir string) error {
		if pid == self {
			return nil
		}
		raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil {
			return err
		}
		cmd := strings.ReplaceAll(strings.TrimRight(string(raw), "\x00"), "\x00", " ")
		if cmd == "" {
			comm, err := os.ReadFile(filepath.Join(dir, "comm"))
			if err != nil {
				return err
			}
			cmd = "[" + strings.TrimSpace(string(comm)) + "]"
		}
		procs = append(procs, cmd)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return procs, nil
}

// readFDs returns the absolute-path targets of dir/fd/*. A descriptor closed
// while reading is skipped.
func readFDs(dir string) ([]string, error) {
	fds, err := os.ReadDir(filepath.Join(dir, "fd"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
		if err != nil {
			if procGone(err) {
				continue
			}
			return nil, err
		}
		if strings.HasPrefix(target, "/") {
			out = append(out, target)
		}
	}
	return out, nil
}
