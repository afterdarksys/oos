package daemon

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/testutil"
)

type fakeWorld struct {
	free    float64
	total   float64
	notes   []string
	ensured int
	now     time.Time
	files   []guard.OpenFile
}

func (w *fakeWorld) deps() Deps {
	return Deps{
		Disk: func(string) (size.DiskUsage, error) {
			return size.DiskUsage{Free: uint64(w.free * size.GB), Total: uint64(w.total * size.GB)}, nil
		},
		Now:       func() time.Time { return w.now },
		Notify:    func(title, msg string) error { w.notes = append(w.notes, msg); return nil },
		OpenFiles: func() ([]guard.OpenFile, error) { return w.files, nil },
		Ensure: func(cfg *config.Config, env guard.Env, target float64, live bool, now time.Time) (plan.EnsureResult, error) {
			w.ensured++
			w.free = target + 1
			return plan.EnsureResult{TargetGB: target, StartFreeGB: 2, FreeGB: w.free, Reached: true, Live: live, Steps: []string{"purged 1 batch"}}, nil
		},
		Log: &bytes.Buffer{},
	}
}

func newTestDaemon(t *testing.T, w *fakeWorld, tune func(*config.Policy)) *Daemon {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	p.MinFreeGB, p.WarnFreeGB = 10, 20
	p.StateFile = filepath.Join(home, "state.json")
	p.Daemon.SizedEveryHours = -1 // no plan walks in these tests
	if tune != nil {
		tune(&p)
	}
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	return New(cfg, "test", guard.Env{Home: home, Procs: testutil.NoProcs}, "t", w.deps())
}

func TestTickAlertsOnChangeAndRateLimits(t *testing.T) {
	w := &fakeWorld{free: 22, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, func(p *config.Policy) { p.AlertHoursToCritical = -1 }) // the rate alert has its own test
	s := d.Tick(w.now)
	if s.Label != "OK" || len(w.notes) != 0 || s.Ticks != 1 {
		t.Fatalf("ok start: %+v notes %v", s, w.notes)
	}
	// warn (a 7 GB step, under the 10 GB drop line): one alert on the change,
	// none on the next tick, one more after the repeat window
	w.free = 15
	w.now = w.now.Add(5 * time.Minute)
	d.Tick(w.now)
	w.now = w.now.Add(5 * time.Minute)
	d.Tick(w.now)
	if len(w.notes) != 1 || !strings.Contains(w.notes[0], "WARN") {
		t.Fatalf("one warn alert, got %v", w.notes)
	}
	w.now = w.now.Add(61 * time.Minute)
	d.Tick(w.now)
	if len(w.notes) != 2 {
		t.Fatalf("repeat after the window, got %v", w.notes)
	}
	// recovery alerts once
	w.free = 50
	w.now = w.now.Add(5 * time.Minute)
	d.Tick(w.now)
	if len(w.notes) != 3 || !strings.Contains(w.notes[2], "back to") {
		t.Fatalf("recovery: %v", w.notes)
	}
	// a 12 GB drop between ticks names the writer
	tmp := filepath.Join(t.TempDir(), "big.log")
	testutil.Write(t, tmp, 1024)
	w.files = []guard.OpenFile{{PID: 42, Command: "cargo", Path: tmp}}
	w.now = w.now.Add(5 * time.Minute)
	d.Tick(w.now) // first sample
	testutil.Write(t, tmp, 4096)
	w.free = 38
	w.now = w.now.Add(5 * time.Minute)
	s = d.Tick(w.now)
	if len(w.notes) != 4 || !strings.Contains(w.notes[3], "dropped 12.0 GB") || !strings.Contains(w.notes[3], "cargo, pid 42") {
		t.Fatalf("drop alert with writer: %v", w.notes)
	}
	if len(s.Writers) != 1 || s.Writers[0].Delta != 3072 {
		t.Errorf("writers in status: %+v", s.Writers)
	}
}

func TestTickAutoActOnlyWhenAllowed(t *testing.T) {
	w := &fakeWorld{free: 5, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, nil)
	d.Tick(w.now)
	if w.ensured != 0 {
		t.Fatal("auto_act is off by default: the daemon must never act")
	}
	w2 := &fakeWorld{free: 5, total: 100, now: time.Now()}
	d2 := newTestDaemon(t, w2, func(p *config.Policy) { p.Daemon.AutoAct = true; p.Daemon.AutoActCooldownMinutes = 30 })
	s := d2.Tick(w2.now)
	if w2.ensured != 1 || s.AutoAct.Last == nil || !s.AutoAct.Last.Reached || s.Label != "OK" || s.FreeGB != 21 {
		t.Fatalf("auto-act under critical: ensured=%d status=%+v", w2.ensured, s)
	}
	found := false
	for _, n := range w2.notes {
		if strings.Contains(n, "auto-act reached 20 GB") {
			found = true
		}
	}
	if !found {
		t.Errorf("auto-act must be announced: %v", w2.notes)
	}
	// critical again inside the cooldown: no second action
	w2.free = 5
	w2.now = w2.now.Add(10 * time.Minute)
	d2.Tick(w2.now)
	if w2.ensured != 1 {
		t.Errorf("cooldown must hold: ensured=%d", w2.ensured)
	}
	w2.now = w2.now.Add(25 * time.Minute)
	d2.Tick(w2.now)
	if w2.ensured != 2 {
		t.Errorf("after the cooldown it may act again: ensured=%d", w2.ensured)
	}
}

func TestTickSurvivesFailures(t *testing.T) {
	w := &fakeWorld{free: 50, total: 100, now: time.Now()}
	d := newTestDaemon(t, w, func(p *config.Policy) { p.Daemon.AutoAct = true })
	d.deps.OpenFiles = func() ([]guard.OpenFile, error) { return nil, errors.New("lsof exploded") }
	w.free = 1
	s := d.Tick(w.now)
	d.deps.Ensure = func(*config.Config, guard.Env, float64, bool, time.Time) (plan.EnsureResult, error) {
		return plan.EnsureResult{}, errors.New("no audit log")
	}
	w.free = 1 // the first tick's fake action refilled it; go critical again past the cooldown
	w.now = w.now.Add(2 * time.Hour)
	s = d.Tick(w.now)
	joined := strings.Join(s.Errors, "\n")
	if !strings.Contains(joined, "lsof exploded") || !strings.Contains(joined, "no audit log") {
		t.Errorf("failures land in status errors, not a crash: %v", s.Errors)
	}
	d.Reload(d.cfg, "reloaded")
	if d.Snapshot().Config != "reloaded" {
		t.Error("reload must update the status")
	}
}
