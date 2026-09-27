//go:build linux

package guard

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeProc builds a procfs look-alike: PID 1 (systemd) and PID 100 share a
// namespace; 100 works in /home/u/work and holds /home/u/cache/x open.
func fakeProc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldRoot, oldMarkers := procRoot, containerMarkers
	procRoot, containerMarkers = root, nil
	t.Cleanup(func() { procRoot, containerMarkers = oldRoot, oldMarkers })
	mk := func(pid, comm, cmdline, cwd string, fds map[string]string) {
		dir := filepath.Join(root, pid)
		must(t, os.MkdirAll(filepath.Join(dir, "fd"), 0o755))
		must(t, os.MkdirAll(filepath.Join(dir, "ns"), 0o755))
		must(t, os.Symlink("pid:[4026531836]", filepath.Join(dir, "ns", "pid")))
		must(t, os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644))
		must(t, os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644))
		if cwd != "" {
			must(t, os.Symlink(cwd, filepath.Join(dir, "cwd")))
		}
		for fd, target := range fds {
			must(t, os.Symlink(target, filepath.Join(dir, "fd", fd)))
		}
	}
	mk("1", "systemd", "/sbin/init\x00splash\x00", "/", map[string]string{"0": "/dev/null"})
	mk("100", "python3", "python3\x00/home/u/my app/run.py\x00--log\x00a\nb\x00", "/home/u/work",
		map[string]string{"0": "/dev/pts/0", "3": "/home/u/cache/x", "4": "socket:[123]"})
	mk("2", "kthreadd", "", "/", nil)
	mk(strconv.Itoa(os.Getpid()), "oos", "oos\x00plan\x00/home/u/cache\x00", "/home/u", nil)
	must(t, os.Symlink(strconv.Itoa(os.Getpid()), filepath.Join(root, "self")))
	return root
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestProcListersReadProc(t *testing.T) {
	fakeProc(t)
	files, err := ListOpenFiles()
	if err != nil || !Referenced("/home/u/cache/x", files) || Referenced("socket", files) {
		t.Fatalf("open files: %q %v", files, err)
	}
	cwds, err := ListProcessCwds()
	if err != nil || !Referenced("/home/u/work", cwds) {
		t.Fatalf("cwds: %q %v", cwds, err)
	}
	procs, err := ListProcesses()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(procs, "|")
	for _, want := range []string{"/sbin/init splash", "python3 /home/u/my app/run.py --log a\nb", "[kthreadd]"} {
		if !strings.Contains(joined, want) {
			t.Errorf("process list %q lacks %q", procs, want)
		}
	}
	if strings.Contains(joined, "oos plan") {
		t.Error("our own command line must not be listed")
	}
	if !Referenced("/home/u/my app", procs) {
		t.Error("path with a space in an argument must be found")
	}
	byProc, err := OpenFilesByProcess()
	if err != nil || len(byProc) != 1 || byProc[0].PID != 100 || byProc[0].Command != "python3" || byProc[0].Path != "/home/u/cache/x" {
		t.Errorf("by process: %+v %v", byProc, err)
	}
}

func TestProcListersFailClosed(t *testing.T) {
	t.Run("no proc", func(t *testing.T) {
		root := fakeProc(t)
		must(t, os.Remove(filepath.Join(root, "self")))
		for name, f := range map[string]func() ([]string, error){"open": ListOpenFiles, "cwd": ListProcessCwds, "procs": ListProcesses} {
			if got, err := f(); err == nil {
				t.Errorf("%s: missing /proc/self must be an error, got %q", name, got)
			}
		}
		if _, err := OpenFilesByProcess(); err == nil {
			t.Error("by process: missing /proc/self must be an error")
		}
	})
	t.Run("hidepid", func(t *testing.T) {
		root := fakeProc(t)
		must(t, os.RemoveAll(filepath.Join(root, "1")))
		if _, err := ListProcesses(); err == nil {
			t.Error("PID 1 invisible means other processes are hidden")
		}
		if _, err := ListOpenFiles(); err == nil {
			t.Error("PID 1 invisible must fail open-file listing")
		}
	})
	t.Run("exited is fine", func(t *testing.T) {
		root := fakeProc(t)
		must(t, os.MkdirAll(filepath.Join(root, "300"), 0o755)) // no cwd, no fd: gone
		if _, err := ListOpenFiles(); err != nil {
			t.Errorf("exited process must be skipped: %v", err)
		}
		if _, err := ListProcessCwds(); err != nil {
			t.Errorf("exited process must be skipped: %v", err)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads everything")
		}
		root := fakeProc(t)
		dir := filepath.Join(root, "100")
		must(t, os.Chmod(dir, 0))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		for name, f := range map[string]func() ([]string, error){"open": ListOpenFiles, "cwd": ListProcessCwds, "procs": ListProcesses} {
			_, err := f()
			var ue *UnreadableProcessesError
			if !errors.As(err, &ue) || ue.Count != 1 {
				t.Errorf("%s: unreadable live process must fail with a count: %v", name, err)
			}
		}
	})
}

func TestProcListersDetectPIDNamespace(t *testing.T) {
	cases := map[string]func(root string){
		"different ns": func(root string) {
			p := filepath.Join(root, "1", "ns", "pid")
			must(t, os.Remove(p))
			must(t, os.Symlink("pid:[4026532999]", p))
		},
		"pid 1 ns unreadable": func(root string) { must(t, os.Remove(filepath.Join(root, "1", "ns", "pid"))) },
		"pid 1 not an init": func(root string) {
			must(t, os.WriteFile(filepath.Join(root, "1", "comm"), []byte("sh\n"), 0o644))
		},
		"container marker": func(root string) {
			m := filepath.Join(root, ".dockerenv")
			must(t, os.WriteFile(m, nil, 0o644))
			containerMarkers = []string{m}
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			root := fakeProc(t)
			breakIt(root)
			for what, f := range map[string]func() ([]string, error){"open": ListOpenFiles, "cwd": ListProcessCwds} {
				if _, err := f(); !errors.Is(err, ErrPIDNamespace) {
					t.Errorf("%s: want ErrPIDNamespace, got %v", what, err)
				}
			}
			// The process list for installer checks still works in a container.
			if _, err := ListProcesses(); err != nil {
				t.Errorf("process list must not need the host namespace: %v", err)
			}
		})
	}
}

// On the real /proc (including Alpine/busybox and slim images without ps)
// the process list works, and the reference listers either answer or say
// exactly why they cannot; they never return an empty "nothing" silently.
func TestRealProcListing(t *testing.T) {
	procs, err := ListProcesses()
	if err != nil || len(procs) == 0 {
		t.Fatalf("process list from /proc: %d %v", len(procs), err)
	}
	for name, f := range map[string]func() ([]string, error){"open": ListOpenFiles, "cwd": ListProcessCwds} {
		got, err := f()
		var ue *UnreadableProcessesError
		switch {
		case err == nil:
			if len(got) == 0 {
				t.Errorf("%s: empty answer from a running system", name)
			}
		case errors.Is(err, ErrPIDNamespace), errors.As(err, &ue):
			t.Logf("%s: %v", name, err)
		default:
			t.Errorf("%s: unexpected error %v", name, err)
		}
	}
}

// Without root, another user's process is off limits by design: it is
// counted, not fatal. Our own uid's processes, and anything when root, must
// be readable.
func TestUnreadableOtherUserProcesses(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything; chmod cannot simulate denial")
	}
	for _, c := range []struct {
		name        string
		euid, owner int
		wantErr     bool
	}{
		{"other uid, not root", 4242, 9999, false},
		{"same uid", 4242, 4242, true},
		{"root", 0, 9999, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := fakeProc(t)
			dir := filepath.Join(root, "100")
			oldE, oldO := procEuid, procOwner
			procEuid = func() int { return c.euid }
			procOwner = func(d string) (int, error) {
				if d == dir {
					return c.owner, nil
				}
				return c.euid, nil
			}
			t.Cleanup(func() { procEuid, procOwner = oldE, oldO })
			must(t, os.Chmod(dir, 0))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
			for name, f := range map[string]func() ([]string, error){"open": ListOpenFiles, "cwd": ListProcessCwds} {
				_, err := f()
				var ue *UnreadableProcessesError
				if c.wantErr {
					if !errors.As(err, &ue) || ue.Count != 1 {
						t.Errorf("%s: want unreadable error, got %v", name, err)
					}
					continue
				}
				if err != nil || OtherUserProcessesNotInspected() != 1 {
					t.Errorf("%s: other-user process must pass with a count: err=%v n=%d", name, err, OtherUserProcessesNotInspected())
				}
			}
		})
	}
}

// Non-root cannot read PID 1's ns link; a host init with no container
// signal is then accepted, while a container cgroup is still refused.
func TestPIDNamespaceWithoutNSLink(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads the ns link; chmod cannot simulate denial")
	}
	root := fakeProc(t)
	ns := filepath.Join(root, "1", "ns")
	must(t, os.Chmod(ns, 0))
	t.Cleanup(func() { _ = os.Chmod(ns, 0o755) })
	if err := checkPIDNamespace(); err != nil {
		t.Fatalf("denied ns link with a host init must pass: %v", err)
	}
	must(t, os.WriteFile(filepath.Join(root, "1", "comm"), []byte("tini\n"), 0o644))
	if err := checkPIDNamespace(); !errors.Is(err, ErrPIDNamespace) {
		t.Errorf("non-init PID 1 must be refused: %v", err)
	}
}

func TestPIDNamespaceContainerCgroup(t *testing.T) {
	root := fakeProc(t)
	if err := checkPIDNamespace(); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	cg := filepath.Join(root, strconv.Itoa(os.Getpid()), "cgroup")
	must(t, os.WriteFile(cg, []byte("0::/system.slice/docker-0123abcd.scope\n"), 0o644))
	if err := checkPIDNamespace(); !errors.Is(err, ErrPIDNamespace) {
		t.Errorf("container cgroup must be refused: %v", err)
	}
	must(t, os.WriteFile(cg, []byte("0::/user.slice/user-1000.slice/session-2.scope\n"), 0o644))
	if err := checkPIDNamespace(); err != nil {
		t.Errorf("a user session cgroup is a host: %v", err)
	}
}
