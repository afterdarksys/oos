// Package daemon is the resident watcher: a tick every few minutes that
// records free space, fits the forecast, names what is writing, alerts on
// state changes (rate-limited), and, only when the policy says so, runs the
// ensure path under critical. --status asks it over a unix socket.
package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/docker"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
)

// Deps are the outside world; tests inject fakes.
type Deps struct {
	Disk      func(string) (size.DiskUsage, error)
	Now       func() time.Time
	Notify    func(title, msg string) error
	OpenFiles func() ([]guard.OpenFile, error)
	Ensure    func(cfg *config.Config, env guard.Env, target float64, live bool, now time.Time) (plan.EnsureResult, error)
	Log       io.Writer
}

// Status is what the socket answers and what --status prints.
type Status struct {
	Version     string         `json:"version"`
	PID         int            `json:"pid"`
	Started     time.Time      `json:"started"`
	Volume      string         `json:"volume"`
	Config      string         `json:"config_source"`
	Interval    string         `json:"interval"`
	Ticks       int            `json:"ticks"`
	LastTick    time.Time      `json:"last_tick"`
	NextTick    time.Time      `json:"next_tick"`
	Busy        string         `json:"busy,omitempty"` // what a long tick is doing right now
	FreeGB      float64        `json:"free_gb"`
	TotalGB     float64        `json:"total_gb"`
	Label       string         `json:"status"`
	Forecast    state.Forecast `json:"forecast"`
	DropGB      float64        `json:"drop_gb_last_tick"`
	Writers     []Writer       `json:"writers,omitempty"`
	WritersAt   time.Time      `json:"writers_at,omitempty"`
	Alerts      int            `json:"alerts_sent"`
	LastAlert   string         `json:"last_alert,omitempty"`
	LastAlertAt time.Time      `json:"last_alert_at,omitempty"`
	Sized       *SizedSummary  `json:"sized,omitempty"`
	AutoAct     AutoActStatus  `json:"auto_act"`
	Errors      []string       `json:"errors,omitempty"`
}

// SizedSummary is the periodic sized check's answer.
type SizedSummary struct {
	At          time.Time        `json:"at"`
	Reclaimable int64            `json:"reclaimable_bytes"`
	Known       map[string]int64 `json:"known_bytes"`
	Docker      *docker.Usage    `json:"docker,omitempty"`
	Elapsed     string           `json:"elapsed"`
}

// AutoActStatus says whether the daemon may act and what it last did.
type AutoActStatus struct {
	Enabled  bool               `json:"enabled"`
	TargetGB float64            `json:"target_gb"`
	LastAt   time.Time          `json:"last_at,omitempty"`
	Last     *plan.EnsureResult `json:"last,omitempty"`
}

// Daemon holds the loop's state. Only Tick mutates the tick-private fields
// (sampler, lastAlert, lastSized, lastAct, prevFree); st is shared with the
// socket and is only touched under mu, and never while anything slow runs.
type Daemon struct {
	mu      sync.Mutex
	cfg     *config.Config
	cfgSrc  string
	env     guard.Env
	deps    Deps
	version string
	st      Status

	sampler   *Sampler
	lastLabel string
	lastAlert map[string]time.Time
	lastSized time.Time
	lastAct   time.Time
	prevFree  float64
	havePrev  bool
}

// New wires a daemon; nothing runs until Tick or Run.
func New(cfg *config.Config, cfgSrc string, env guard.Env, version string, deps Deps) *Daemon {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Disk == nil {
		deps.Disk = size.Disk
	}
	if deps.Ensure == nil {
		deps.Ensure = func(cfg *config.Config, env guard.Env, target float64, live bool, now time.Time) (plan.EnsureResult, error) {
			return plan.Ensure(cfg, env, target, nil, live, now)
		}
	}
	if deps.Log == nil {
		deps.Log = io.Discard
	}
	d := &Daemon{cfg: cfg, cfgSrc: cfgSrc, env: env, deps: deps, version: version, sampler: NewSampler(), lastAlert: map[string]time.Time{}}
	d.st = Status{Version: version, PID: os.Getpid(), Started: deps.Now(), Volume: cfg.Volume, Config: cfgSrc, Interval: cfg.Policy.Daemon.Interval().String()}
	d.st.AutoAct = AutoActStatus{Enabled: cfg.Policy.Daemon.AutoAct, TargetGB: cfg.Policy.Daemon.Target(cfg.Policy)}
	return d
}

// config returns the current config under the lock (SIGHUP may swap it).
func (d *Daemon) config() *config.Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg
}

// set mutates the shared status under the lock.
func (d *Daemon) set(f func(s *Status)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(&d.st)
}

// Reload swaps the config (SIGHUP). Counters and samples survive.
func (d *Daemon) Reload(cfg *config.Config, src string) {
	d.mu.Lock()
	d.cfg, d.cfgSrc = cfg, src
	d.st.Config = src
	d.st.Volume = cfg.Volume
	d.st.Interval = cfg.Policy.Daemon.Interval().String()
	d.st.AutoAct.Enabled = cfg.Policy.Daemon.AutoAct
	d.st.AutoAct.TargetGB = cfg.Policy.Daemon.Target(cfg.Policy)
	d.mu.Unlock()
	d.logf("config reloaded from %s", src)
}

// Snapshot is the status as of the last tick. It never waits on a walk.
func (d *Daemon) Snapshot() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.st
	s.Writers = append([]Writer(nil), d.st.Writers...)
	s.Errors = append([]string(nil), d.st.Errors...)
	return s
}

func (d *Daemon) logf(format string, a ...any) {
	fmt.Fprintf(d.deps.Log, "%s %s\n", d.deps.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
}

// alert sends a rate-limited notification: one per kind per alert_repeat
// window unless force (a state change) is set.
func (d *Daemon) alert(dp config.DaemonPolicy, kind, title, msg string, now time.Time, force bool) {
	if last, ok := d.lastAlert[kind]; ok && !force && now.Sub(last) < dp.AlertRepeat() {
		return
	}
	d.lastAlert[kind] = now
	d.set(func(s *Status) {
		s.Alerts++
		s.LastAlert = kind + ": " + msg
		s.LastAlertAt = now
	})
	d.logf("alert %s: %s", kind, msg)
	if d.deps.Notify != nil {
		if err := d.deps.Notify(title, msg); err != nil {
			d.note("notify: " + err.Error())
		}
	}
}

func (d *Daemon) note(msg string) {
	d.set(func(s *Status) {
		s.Errors = append(s.Errors, msg)
		if len(s.Errors) > 20 {
			s.Errors = s.Errors[len(s.Errors)-20:]
		}
	})
	d.logf("error: %s", msg)
}

func (d *Daemon) busy(what string) { d.set(func(s *Status) { s.Busy = what }) }

// Tick is one iteration. Exported so tests drive it with a fake clock.
// Slow work (open-file sampling, the sized walk, acting) runs without the
// lock so --status answers during it.
func (d *Daemon) Tick(now time.Time) Status {
	cfg := d.config()
	p := cfg.Policy
	dp := p.Daemon
	d.set(func(s *Status) {
		s.Ticks++
		s.LastTick = now
		s.NextTick = now.Add(dp.Interval())
	})

	du, err := d.deps.Disk(cfg.Volume)
	if err != nil {
		d.note("statfs " + cfg.Volume + ": " + err.Error())
		return d.Snapshot()
	}
	label, _ := status.Of(p, du)
	free := du.FreeGB()

	st, _ := state.Load(p.StateFile)
	st.Volume = cfg.Volume
	st.Record("daemon", du, now)
	if err := state.Save(p.StateFile, st); err != nil {
		d.note("save state: " + err.Error())
	}
	fc := st.Forecast(now, p.ForecastWindow(), p.WarnFreeGB, p.MinFreeGB)

	var drop float64
	if d.havePrev {
		drop = d.prevFree - free
	}
	d.prevFree, d.havePrev = free, true
	d.set(func(s *Status) {
		s.FreeGB, s.TotalGB, s.Label, s.Forecast, s.DropGB = free, du.TotalGB(), label, fc, drop
	})

	// what grew, sampled every tick so a drop can name its writer
	var writers []Writer
	if dp.WantWriter() && d.deps.OpenFiles != nil {
		d.busy("sampling open files")
		if files, err := d.deps.OpenFiles(); err != nil {
			d.note("open files: " + err.Error())
		} else {
			writers = d.sampler.Sample(files, now, dp.TopN())
			d.set(func(s *Status) { s.Writers, s.WritersAt = writers, now })
		}
		d.busy("")
	}

	changed := label != d.lastLabel
	switch {
	case label != "OK":
		d.alert(dp, "status", "oos: disk "+label, fmt.Sprintf("%.1f GB free of %.1f GB (%s)", free, du.TotalGB(), label), now, changed)
	case changed && d.lastLabel != "" && d.lastLabel != "OK":
		d.alert(dp, "recovered", "oos: disk OK", fmt.Sprintf("back to %.1f GB free", free), now, true)
	}
	d.lastLabel = label

	threshold := p.AlertDropGB
	if threshold <= 0 {
		threshold = 10
	}
	if drop >= threshold {
		msg := fmt.Sprintf("dropped %.1f GB since the last tick (%s)", drop, dp.Interval())
		if len(writers) > 0 {
			msg += "; writing: " + describeWriters(writers, 3)
		} else {
			msg += "; run oos -d to see what grew"
		}
		d.alert(dp, "drop", "oos: disk filling", msg, now, false)
	}
	if h := p.AlertHours(); h > 0 && fc.HoursToCritical > 0 && fc.HoursToCritical <= h {
		msg := fmt.Sprintf("critical in %.1fh at %+.2f GB/h", fc.HoursToCritical, fc.RateGBPerHour)
		if len(writers) > 0 {
			msg += "; writing: " + describeWriters(writers, 3)
		}
		d.alert(dp, "forecast", "oos: disk "+label, msg, now, false)
	}

	// expired quarantine, as the hourly tick does
	if p.Quarantine && p.AgentPurgeExpired {
		olderThan := time.Duration(p.QuarantineDays) * 24 * time.Hour
		if freed, names, perr := plan.PurgeBatches(p.QuarantineDir, olderThan, now, false); len(names) > 0 || perr != nil {
			d.logf("purge expired quarantine: freed=%s batches=%s err=%v", size.Human(freed), strings.Join(names, ","), perr)
		}
	}

	// act only when told to, only under critical, only after the cooldown
	if dp.AutoAct && label == "CRITICAL" && (d.lastAct.IsZero() || now.Sub(d.lastAct) >= dp.Cooldown()) {
		d.lastAct = now
		target := dp.Target(p)
		d.busy("acting: ensure " + fmt.Sprintf("%.0f GB", target))
		res, err := d.deps.Ensure(cfg, d.env, target, true, now)
		d.busy("")
		d.set(func(s *Status) { s.AutoAct.LastAt = now; s.AutoAct.Last = &res })
		if err != nil {
			d.note("auto-act: " + err.Error())
			d.alert(dp, "auto-act", "oos: could not act", err.Error(), now, true)
		} else {
			verb := "reached"
			if !res.Reached {
				verb = "did not reach"
			}
			msg := fmt.Sprintf("auto-act %s %.0f GB: %.1f -> %.1f GB free; %d steps", verb, target, res.StartFreeGB, res.FreeGB, len(res.Steps))
			d.logf("%s: %s", msg, strings.Join(res.Steps, " | "))
			d.alert(dp, "auto-act", "oos: acted under critical", msg, now, true)
			if du2, err := d.deps.Disk(cfg.Volume); err == nil {
				label2, _ := status.Of(p, du2)
				d.prevFree = du2.FreeGB()
				d.set(func(s *Status) { s.FreeGB, s.Label = du2.FreeGB(), label2 })
			}
		}
	}

	// periodic sized refresh: known entries and docker, into the state file
	if every := dp.SizedEvery(); every > 0 && (d.lastSized.IsZero() || now.Sub(d.lastSized) >= every) {
		d.lastSized = now
		d.busy("sizing known entries")
		d.sized(cfg, now)
		d.busy("")
	}
	return d.Snapshot()
}

func (d *Daemon) sized(cfg *config.Config, now time.Time) {
	start := d.deps.Now()
	items := plan.Build(cfg, d.env, nil, now)
	sum := &SizedSummary{At: now, Known: map[string]int64{}}
	for _, it := range items {
		if it.Refused == nil || it.Bytes > 0 {
			sum.Known[it.Path] = it.Bytes
		}
		if it.Refused == nil && config.IsDestructive(it.Action) {
			sum.Reclaimable += it.Reclaimable
		}
	}
	if st, err := state.Load(cfg.Policy.StateFile); err == nil {
		for k, v := range sum.Known {
			st.Known[k] = v
		}
		_ = state.Save(cfg.Policy.StateFile, st)
	}
	if want, _ := docker.Wanted(cfg.Policy); want {
		if u, err := docker.Collect(docker.Timeout(cfg.Policy)); err == nil {
			sum.Docker = u
		} else {
			d.note("docker: " + err.Error())
		}
	}
	sum.Elapsed = d.deps.Now().Sub(start).Round(time.Second).String()
	d.set(func(s *Status) { s.Sized = sum })
	d.logf("sized %d entries in %s; reclaimable %s", len(items), sum.Elapsed, size.Human(sum.Reclaimable))
}

func describeWriters(ws []Writer, n int) string {
	if len(ws) > n {
		ws = ws[:n]
	}
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		parts = append(parts, fmt.Sprintf("%s (%s, pid %d) +%s", w.Path, w.Command, w.PID, size.Human(w.Delta)))
	}
	return strings.Join(parts, ", ")
}

// Run ticks until ctx ends. The first tick is immediate.
func (d *Daemon) Run(ctx context.Context) {
	cfg := d.config()
	d.logf("oos daemon %s started on %s, interval %s, socket %q, auto_act=%v", d.version, cfg.Volume, cfg.Policy.Daemon.Interval(), cfg.Policy.Daemon.SocketPath(d.env.Home), cfg.Policy.Daemon.AutoAct)
	d.Tick(d.deps.Now())
	for {
		wait := d.config().Policy.Daemon.Interval()
		select {
		case <-ctx.Done():
			d.logf("stopping: %v", ctx.Err())
			return
		case <-time.After(wait):
			d.Tick(d.deps.Now())
		}
	}
}
