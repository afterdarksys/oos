package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/mutation"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/safefs"
)

func TestDegradedRunsAlertOnlyAndRecovers(t *testing.T) {
	w := &fakeWorld{free: 5, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, func(p *config.Policy) { p.Daemon.AutoAct = true })
	good := d.cfg
	loads := 0
	var loadErr error = errors.New(`parse config: unknown key "future"`)
	d.deps.LoadConfig = func() (*config.Config, string, error) {
		loads++
		if loadErr != nil {
			return nil, "", loadErr
		}
		c := *good
		c.Policy.Daemon.AutoAct = true
		return &c, "fixed.json", nil
	}
	d.SetDegraded(loadErr)
	d.SetDegraded(loadErr) // a second rejection does not notify again
	s := d.Tick(w.now)
	if w.ensured != 0 {
		t.Fatal("a degraded daemon must never act")
	}
	if !s.Degraded || !strings.Contains(s.ConfigError, "future") || s.AutoAct.Enabled {
		t.Fatalf("status must say degraded: %+v", s)
	}
	rejected := 0
	for _, n := range w.notes {
		if strings.Contains(n, "config rejected") && strings.Contains(n, "alert-only") {
			rejected++
		}
	}
	if rejected != 1 {
		t.Fatalf("one config-rejected notification, got %v", w.notes)
	}
	if !strings.Contains(strings.Join(w.notes, "\n"), "CRITICAL") {
		t.Errorf("degraded still alerts on the disk: %v", w.notes)
	}
	// inside the retry window: no reload attempt
	w.now = w.now.Add(5 * time.Minute)
	d.Tick(w.now)
	if loads != 0 {
		t.Fatalf("retry before %s: loads=%d", configRetry, loads)
	}
	// past it, still broken: one attempt, still degraded
	w.now = w.now.Add(6 * time.Minute)
	d.Tick(w.now)
	if loads != 1 || !d.Degraded() {
		t.Fatalf("retry: loads=%d degraded=%v", loads, d.Degraded())
	}
	// fixed config, via SIGHUP (TryReload): back to normal, acting again
	loadErr = nil
	if err := d.TryReload(); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(time.Minute)
	s = d.Tick(w.now)
	if s.Degraded || s.ConfigError != "" || s.Config != "fixed.json" || w.ensured != 1 {
		t.Fatalf("recovered: ensured=%d %+v", w.ensured, s)
	}
}

func TestReloadFailureKeepsRunningConfigButReportsIt(t *testing.T) {
	w := &fakeWorld{free: 50, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, nil)
	d.deps.LoadConfig = func() (*config.Config, string, error) { return nil, "", errors.New("bad push") }
	_ = d.TryReload()
	s := d.Snapshot()
	if s.Degraded || s.ConfigError != "bad push" || s.Config != "test" {
		t.Fatalf("a valid running config stays; the rejection is visible: %+v", s)
	}
}

func TestPersistFailureDoesNotLatchPause(t *testing.T) {
	w := &fakeWorld{free: 5, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, func(p *config.Policy) { p.Daemon.AutoAct = true; p.Daemon.AutoActCooldownMinutes = 30 })
	rec := d.cfg.Policy.StateFile + ".auto-act.json"
	// a non-empty directory where the record goes: every write fails
	if err := os.MkdirAll(filepath.Join(rec, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := d.Tick(w.now)
	if w.ensured != 1 || s.AutoAct.Paused || s.AutoAct.PersistError == "" {
		t.Fatalf("an unwritable record must not pause auto-act: ensured=%d %+v", w.ensured, s.AutoAct)
	}
	// the cooldown holds in memory
	w.free = 5
	w.now = w.now.Add(10 * time.Minute)
	d.Tick(w.now)
	if w.ensured != 1 {
		t.Fatalf("in-memory cooldown: ensured=%d", w.ensured)
	}
	// once writable, the next tick persists it
	os.RemoveAll(rec)
	w.now = w.now.Add(time.Minute)
	s = d.Tick(w.now)
	if s.AutoAct.PersistError != "" {
		t.Fatalf("retry should have persisted: %+v", s.AutoAct)
	}
	if _, err := os.Stat(rec); err != nil {
		t.Fatal("the record must be written on retry")
	}
}

func TestBusyLockKeepsLastResult(t *testing.T) {
	w := &fakeWorld{free: 5, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, func(p *config.Policy) { p.Daemon.AutoAct = true })
	d.deps.Ensure = func(*config.Config, guard.Env, float64, bool, time.Time) (plan.EnsureResult, error) {
		return plan.EnsureResult{}, fmt.Errorf("%w (lock x)", mutation.ErrBusy)
	}
	if s := d.Tick(w.now); s.AutoAct.Last != nil || !s.AutoAct.LastAt.IsZero() {
		t.Fatalf("a busy lock did nothing and must not record an empty result: %+v", s.AutoAct)
	}
}

func TestEAGAINIsAFailureNotBusy(t *testing.T) {
	if lockBusy(errors.New("fork: resource temporarily unavailable")) || lockBusy(fmt.Errorf("x: %w", errors.ErrUnsupported)) {
		t.Fatal("only mutation.ErrBusy is busy")
	}
	if !lockBusy(fmt.Errorf("wrapped: %w", mutation.ErrBusy)) {
		t.Fatal("wrapped ErrBusy is busy")
	}
}

func TestPartialAndRefusalAreNotBrakeFailures(t *testing.T) {
	for name, e := range map[string]error{
		"deadline": fmt.Errorf("%w: %w", plan.ErrPartial, context.DeadlineExceeded),
		"refused":  fmt.Errorf("rename: %w", safefs.ErrNoReplaceUnsupported),
	} {
		t.Run(name, func(t *testing.T) {
			w := &fakeWorld{free: 5, total: 100, now: time.Now()}
			d := newTestDaemon(t, w, func(p *config.Policy) {
				p.Daemon.AutoAct = true
				p.Daemon.AutoActCooldownMinutes = 1
				p.Daemon.RecoveryFailureLimit = 1
			})
			d.deps.Ensure = func(*config.Config, guard.Env, float64, bool, time.Time) (plan.EnsureResult, error) {
				w.ensured++
				return plan.EnsureResult{Steps: []string{"partial"}}, e
			}
			for i := 0; i < 3; i++ {
				d.Tick(w.now)
				w.now = w.now.Add(2 * time.Minute)
			}
			s := d.Snapshot()
			if s.AutoAct.Paused || s.AutoAct.Failures != 0 || w.ensured != 3 {
				t.Fatalf("must not trip the brake: ensured=%d %+v", w.ensured, s.AutoAct)
			}
			could, refused := 0, 0
			for _, n := range w.notes {
				if strings.Contains(n, "no-replace") || strings.Contains(n, "quarantine unavailable") || strings.Contains(n, "rename:") {
					refused++
				}
				if strings.Contains(n, "partial run") {
					could++
				}
			}
			if could != 0 {
				t.Errorf("a partial run is not a 'could not act' alert: %v", w.notes)
			}
			if name == "refused" && refused != 1 {
				t.Errorf("a refusal alerts exactly once: %v", w.notes)
			}
		})
	}
}

func TestScheduledPurgeIsHourlyCancellableAndSkippedWhenEmpty(t *testing.T) {
	w := &fakeWorld{free: 50, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, func(p *config.Policy) { p.Quarantine = true; p.AgentPurgeExpired = true })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.env.Ctx = ctx
	batches, purges := 0, 0
	var gotCtx context.Context
	d.deps.Batches = func(*config.Config) (int, error) { return batches, nil }
	d.deps.Purge = func(c context.Context, _ *config.Config, _ time.Time) (int64, []string, error) {
		purges++
		gotCtx = c
		return 0, nil, nil
	}
	d.Tick(w.now)
	if purges != 0 {
		t.Fatal("no batches: no purge (and no lock)")
	}
	batches = 2
	w.now = w.now.Add(5 * time.Minute)
	d.Tick(w.now)
	if purges != 1 || gotCtx != ctx {
		t.Fatalf("first purge with the daemon context: purges=%d", purges)
	}
	for i := 0; i < 5; i++ {
		w.now = w.now.Add(5 * time.Minute)
		d.Tick(w.now)
	}
	if purges != 1 {
		t.Fatalf("at most hourly: purges=%d", purges)
	}
	// a restart inside the hour remembers the last purge
	d2 := New(d.cfg, "test", guard.Env{Home: d.env.Home, Procs: d.env.Procs}, "t", d.deps)
	d2.Tick(w.now)
	if purges != 1 {
		t.Fatalf("restart must honour the hourly limit: purges=%d", purges)
	}
	w.now = w.now.Add(time.Hour)
	d2.Tick(w.now)
	if purges != 2 {
		t.Fatalf("after an hour it purges again: purges=%d", purges)
	}
}

func TestJitterBoundsAndUTCLog(t *testing.T) {
	w := &fakeWorld{free: 50, total: 100, now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("X", 3600))}
	d := newTestDaemon(t, w, nil)
	d.deps.Jitter = func(max time.Duration) time.Duration { return max - 1 }
	if got := d.jittered(100 * time.Minute); got < 90*time.Minute || got >= 110*time.Minute {
		t.Fatalf("+/-10%%: %s", got)
	}
	d.deps.Jitter = func(time.Duration) time.Duration { return 0 }
	if got := d.jittered(100 * time.Minute); got != 90*time.Minute {
		t.Fatalf("low end: %s", got)
	}
	d.logf("path %q", "/a b")
	line := d.deps.Log.(*bytes.Buffer).String()
	if !regexp.MustCompile(`(?m)^2026-01-02T02:04:05Z path "/a b"$`).MatchString(line) {
		t.Fatalf("log lines are RFC3339 UTC: %q", line)
	}
}

func TestRunWaitsForFirstJitteredTick(t *testing.T) {
	w := &fakeWorld{free: 50, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, nil)
	var asked time.Duration
	d.deps.Jitter = func(max time.Duration) time.Duration {
		if asked == 0 {
			asked = max
		}
		return time.Hour // never reached in this test
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	d.Run(ctx)
	if d.Snapshot().Ticks != 0 {
		t.Fatal("the first tick waits out its random offset")
	}
	if asked != d.cfg.Policy.Daemon.Interval() {
		t.Fatalf("first offset drawn from [0, interval): asked %s", asked)
	}
}

func shortSock(t *testing.T) string {
	dir, err := os.MkdirTemp("", "oosd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

func TestSocketLockStopsTheStartupRace(t *testing.T) {
	sock := shortSock(t)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	// not serving yet: without the lock the second daemon's Query would
	// fail and it would remove and steal the socket
	start := time.Now()
	if ln2, err := Listen(sock); !errors.Is(err, ErrRunning) {
		if ln2 != nil {
			ln2.Close()
		}
		t.Fatalf("second daemon must get ErrRunning, got %v", err)
	}
	if time.Since(start) > 900*time.Millisecond {
		t.Error("the lock answers without waiting for a query timeout")
	}
	if fi, err := os.Lstat(sock); err != nil || fi.Mode().Type() != os.ModeSocket {
		t.Fatal("the first daemon's socket must survive")
	}
	ln.Close()
	if _, err := os.Lstat(sock); err == nil {
		t.Fatal("close removes our own socket")
	}
	ln3, err := Listen(sock)
	if err != nil {
		t.Fatalf("the lock is released on close: %v", err)
	}
	ln3.Close()
}

func TestShutdownNeverUnlinksASuccessorsSocket(t *testing.T) {
	sock := shortSock(t)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	// someone replaced the socket file (a successor after a forced unlink)
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	other, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	other.(*net.UnixListener).SetUnlinkOnClose(false)
	defer other.Close()
	ln.Close()
	if fi, err := os.Lstat(sock); err != nil || fi.Mode().Type() != os.ModeSocket {
		t.Fatal("shutdown must not remove a socket it did not create")
	}
}
