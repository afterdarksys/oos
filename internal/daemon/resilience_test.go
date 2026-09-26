package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/size"
)

func TestTickSurvivesUnreadableStateWithoutOverwriting(t *testing.T) {
	w := &fakeWorld{free: 50, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, nil)
	// a directory where the state file should be: unreadable, and must stay as it is
	if err := os.Mkdir(d.cfg.Policy.StateFile, 0o755); err != nil {
		t.Fatal(err)
	}
	s := d.Tick(w.now)
	if s.Label != "OK" || !strings.Contains(strings.Join(s.Errors, "\n"), "state:") {
		t.Fatalf("unreadable state must be reported, not fatal: %+v", s)
	}
	if fi, err := os.Stat(d.cfg.Policy.StateFile); err != nil || !fi.IsDir() {
		t.Fatal("the unreadable state path was overwritten")
	}
}

func TestSafeTickRecoversPanics(t *testing.T) {
	w := &fakeWorld{free: 50, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, nil)
	d.deps.Disk = func(string) (size.DiskUsage, error) { panic("statfs blew up") }
	d.safeTick()
	s := d.Snapshot()
	if !strings.Contains(strings.Join(s.Errors, "\n"), "tick panic: statfs blew up") || s.Busy != "" {
		t.Fatalf("a panicking tick must be recorded, not fatal: %+v", s)
	}
}

func TestCooldownSurvivesRestart(t *testing.T) {
	w := &fakeWorld{free: 5, total: 100, now: time.Now()}
	auto := func(p *config.Policy) { p.Daemon.AutoAct = true; p.Daemon.AutoActCooldownMinutes = 30 }
	d := newTestDaemon(t, w, auto)
	d.Tick(w.now)
	if w.ensured != 1 {
		t.Fatalf("first action: ensured=%d", w.ensured)
	}
	// a restarted daemon on the same state, inside the cooldown, must not act
	w.free = 5
	w.now = w.now.Add(10 * time.Minute)
	d2 := New(d.cfg, "test", guard.Env{Home: d.env.Home, Procs: d.env.Procs}, "t", w.deps())
	if d2.Snapshot().AutoAct.LastAt.IsZero() {
		t.Error("the restored last action must show in status")
	}
	d2.Tick(w.now)
	if w.ensured != 1 {
		t.Fatalf("restart must honour the cooldown: ensured=%d", w.ensured)
	}
	w.now = w.now.Add(25 * time.Minute)
	d2.Tick(w.now)
	if w.ensured != 2 {
		t.Fatalf("after the cooldown it may act: ensured=%d", w.ensured)
	}
}

func TestLockBusyIsNotAFailure(t *testing.T) {
	w := &fakeWorld{free: 5, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, func(p *config.Policy) {
		p.Daemon.AutoAct = true
		p.Daemon.AutoActCooldownMinutes = 30
		p.Daemon.RecoveryFailureLimit = 1
	})
	calls := 0
	d.deps.Ensure = func(*config.Config, guard.Env, float64, bool, time.Time) (plan.EnsureResult, error) {
		calls++
		return plan.EnsureResult{}, fmt.Errorf("another oos mutation is running: %w", syscall.EWOULDBLOCK)
	}
	for i := 0; i < 3; i++ {
		d.Tick(w.now)
		w.now = w.now.Add(5 * time.Minute)
	}
	s := d.Snapshot()
	if s.AutoAct.Paused || s.AutoAct.Failures != 0 {
		t.Fatalf("a busy lock must not trip the brake: %+v", s.AutoAct)
	}
	if calls != 3 {
		t.Errorf("a busy lock must not start the cooldown: calls=%d", calls)
	}
	for _, n := range w.notes {
		if strings.Contains(n, "another oos mutation") {
			t.Errorf("a busy lock is not an alert: %v", w.notes)
		}
	}
}

func TestNoActionOnceStopping(t *testing.T) {
	w := &fakeWorld{free: 5, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, func(p *config.Policy) { p.Daemon.AutoAct = true })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.env.Ctx = ctx
	d.Tick(w.now)
	if w.ensured != 0 {
		t.Fatal("a stopping daemon must not start a cleanup")
	}
}

func TestRunThreadsContextIntoEnsure(t *testing.T) {
	w := &fakeWorld{free: 5, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, func(p *config.Policy) { p.Daemon.AutoAct = true })
	ctx, cancel := context.WithCancel(context.Background())
	var got context.Context
	d.deps.Ensure = func(_ *config.Config, env guard.Env, _ float64, _ bool, _ time.Time) (plan.EnsureResult, error) {
		got = env.Context()
		cancel()
		return plan.EnsureResult{}, context.Canceled
	}
	d.Run(ctx)
	if got == nil || got.Err() == nil {
		t.Fatal("Ensure must see the daemon's context")
	}
	if s := d.Snapshot(); s.AutoAct.Failures != 0 || s.AutoAct.Paused {
		t.Errorf("a cancelled run is not a recovery failure: %+v", s.AutoAct)
	}
}

func TestListenRefusesNonSocket(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d.sock")
	if err := os.WriteFile(p, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ln, err := Listen(p); err == nil {
		ln.Close()
		t.Fatal("a regular file at the socket path must be refused")
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "keep" {
		t.Fatal("a non-socket at the socket path must never be removed")
	}
}

func TestListenReportsRunningDaemon(t *testing.T) {
	dir, err := os.MkdirTemp("", "oosd") // short: unix socket paths are limited to ~104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	w := &fakeWorld{free: 50, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, nil)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeOn(ctx, ln, sock, d) }()
	t.Cleanup(func() { cancel(); <-done })
	if ln2, err := Listen(sock); !errors.Is(err, ErrRunning) {
		if ln2 != nil {
			ln2.Close()
		}
		t.Fatalf("a second daemon must get ErrRunning, got %v", err)
	}
}
