// Package daemon is the resident watcher: a tick every few minutes that
// records free space, fits the forecast, names what is writing, alerts on
// state changes (rate-limited), and, only when the policy says so, runs the
// ensure path under critical. --status asks it over a unix socket.
package daemon

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
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
	// Purge releases expired quarantine batches; ctx is the daemon's, so
	// SIGTERM interrupts it. Batches reports whether there is anything to purge.
	Purge   func(ctx context.Context, cfg *config.Config, now time.Time) (int64, []string, error)
	Batches func(cfg *config.Config) (int, error)
	// LoadConfig re-reads the config (SIGHUP, and the retry while the config
	// on disk is rejected). Nil means there is nothing to reload from.
	LoadConfig func() (*config.Config, string, error)
	// Jitter returns a random duration in [0, max); tests pin it.
	Jitter func(max time.Duration) time.Duration
	Log    io.Writer
}

// configRetry is how often a rejected config is re-read without a SIGHUP.
const configRetry = 10 * time.Minute

// purgeEvery bounds the scheduled quarantine purge to once an hour.
const purgeEvery = time.Hour

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
	// ConfigError is set when the config on disk was rejected. With Degraded
	// the daemon runs alert-only on the embedded default thresholds.
	ConfigError string    `json:"config_error,omitempty"`
	Degraded    bool      `json:"degraded,omitempty"`
	LastPurge   time.Time `json:"last_purge,omitempty"`
	Errors      []string  `json:"errors,omitempty"`
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
	Paused   bool               `json:"paused"`
	Reason   string             `json:"pause_reason,omitempty"`
	Failures int                `json:"consecutive_recovery_failures"`
	Enabled  bool               `json:"enabled"`
	TargetGB float64            `json:"target_gb"`
	LastAt   time.Time          `json:"last_at,omitempty"`
	Last     *plan.EnsureResult `json:"last,omitempty"`
	// PersistError is the last failure writing .auto-act.json; the brake and
	// cooldown are kept in memory meanwhile and the write is retried.
	PersistError string `json:"persist_error,omitempty"`
}

// Daemon holds the loop's state. Only Tick mutates the tick-private fields
// (sampler, lastAlert, lastSized, recovery, prevFree); st is shared with the
// socket and is only touched under mu, and never while anything slow runs.
// The last auto-act time lives in recovery so a restart honours the cooldown.
type Daemon struct {
	recovery RecoveryBrake
	mu       sync.Mutex
	cfg      *config.Config
	cfgSrc   string
	env      guard.Env
	deps     Deps
	version  string
	st       Status

	sampler       *Sampler
	lastLabel     string
	lastAlert     map[string]time.Time
	nextSized     time.Time
	reload        chan struct{}
	prevFree      float64
	havePrev      bool
	recoveryDirty bool // .auto-act.json is behind memory; retry the write
	refusedOnce   bool // a filesystem refusal was already alerted

	// under mu: config rejection state
	degraded    bool
	cfgErr      string
	cfgTried    time.Time
	cfgNotified bool
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
	if deps.Purge == nil {
		deps.Purge = purgeExpired
	}
	if deps.Batches == nil {
		deps.Batches = func(cfg *config.Config) (int, error) {
			bs, err := plan.ListStoreBatches(cfg.Policy)
			return len(bs), err
		}
	}
	if deps.Jitter == nil {
		deps.Jitter = func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			return rand.N(max)
		}
	}
	d := &Daemon{cfg: cfg, cfgSrc: cfgSrc, env: env, deps: deps, version: version, sampler: NewSampler(), lastAlert: map[string]time.Time{}, reload: make(chan struct{}, 1)}
	d.st = Status{Version: version, PID: os.Getpid(), Started: deps.Now(), Volume: cfg.Volume, Config: cfgSrc, Interval: cfg.Policy.Daemon.Interval().String()}
	d.st.AutoAct = AutoActStatus{Enabled: cfg.Policy.Daemon.AutoAct, TargetGB: cfg.Policy.Daemon.Target(cfg.Policy)}
	d.loadRecovery()
	return d
}

// SetDegraded records that the config on disk was rejected and the daemon
// runs on cfg (the embedded default, auto_act off) until a load succeeds.
// It notifies once; the load is retried on SIGHUP and every configRetry.
func (d *Daemon) SetDegraded(err error) {
	now := d.deps.Now()
	d.mu.Lock()
	d.degraded = true
	d.cfgErr = err.Error()
	d.cfgTried = now
	d.cfg.Policy.Daemon.AutoAct = false
	d.st.ConfigError, d.st.Degraded = d.cfgErr, true
	d.st.AutoAct.Enabled = false
	first := !d.cfgNotified
	d.cfgNotified = true
	d.mu.Unlock()
	msg := "oos: config rejected: " + err.Error() + "; running alert-only"
	d.logf("%s", msg)
	if first && d.deps.Notify != nil {
		if nerr := d.deps.Notify("oos: config rejected", msg); nerr != nil {
			d.note("notify: " + nerr.Error())
		}
	}
}

// TryReload re-reads the config through Deps.LoadConfig. Success swaps it
// in and clears any rejection. Failure keeps the running config (a valid one
// stays in force; a degraded daemon stays alert-only) and records why.
func (d *Daemon) TryReload() error {
	if d.deps.LoadConfig == nil {
		return nil
	}
	now := d.deps.Now()
	d.mu.Lock()
	d.cfgTried = now
	d.mu.Unlock()
	cfg, src, err := d.deps.LoadConfig()
	if err != nil {
		d.mu.Lock()
		d.cfgErr = err.Error()
		d.st.ConfigError = d.cfgErr
		degraded := d.degraded
		d.mu.Unlock()
		if degraded {
			d.logf("config still rejected, staying alert-only: %v", err)
		} else {
			d.logf("reload failed, keeping the running config: %v", err)
		}
		return err
	}
	d.mu.Lock()
	wasDegraded := d.degraded
	d.degraded, d.cfgErr = false, ""
	d.st.ConfigError, d.st.Degraded = "", false
	d.mu.Unlock()
	if wasDegraded {
		// the brake file lives beside the real state file, which may differ
		// from the default's; read it before auto-act can run
		d.mu.Lock()
		d.cfg = cfg
		d.mu.Unlock()
		d.loadRecoveryLocked()
	}
	d.Reload(cfg, src)
	return nil
}

// retryConfig re-reads a rejected config at most every configRetry.
func (d *Daemon) retryConfig(now time.Time) {
	d.mu.Lock()
	due := d.cfgErr != "" && (d.cfgTried.IsZero() || now.Sub(d.cfgTried) >= configRetry || now.Before(d.cfgTried))
	d.mu.Unlock()
	if due {
		_ = d.TryReload()
	}
}

// Degraded reports whether the daemon is running alert-only.
func (d *Daemon) Degraded() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.degraded
}

// loadRecoveryLocked is loadRecovery with the shared status under the lock.
func (d *Daemon) loadRecoveryLocked() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.recovery = RecoveryBrake{}
	d.loadRecovery()
}

// purgeExpired is the daemon's scheduled purge: the plan's scheduled purge,
// but with the daemon's context so SIGTERM interrupts it at a safe point.
func purgeExpired(ctx context.Context, cfg *config.Config, now time.Time) (int64, []string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	l, err := plan.Mutation(cfg)
	if err != nil {
		return 0, nil, err
	}
	defer l.Close()
	log, err := state.OpenLog(cfg.Policy.LogFile)
	if err != nil {
		return 0, nil, err
	}
	defer log.Close()
	if _, err = fmt.Fprintf(log, "%s scheduled purge intent source=daemon\n", now.UTC().Format(time.RFC3339)); err != nil {
		return 0, nil, err
	}
	if err = log.Sync(); err != nil {
		return 0, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Policy.OperationTimeout())
	defer cancel()
	n, names, err := plan.PurgeStores(ctx, cfg.Policy, time.Duration(cfg.Policy.QuarantineDays)*24*time.Hour, now, false, false)
	fmt.Fprintf(log, "%s scheduled purge source=daemon recorded=%d batches=%q err=%v\n", now.UTC().Format(time.RFC3339), n, names, err)
	return n, names, err
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
	d.st.AutoAct.Enabled = cfg.Policy.Daemon.AutoAct && !d.degraded
	d.st.AutoAct.TargetGB = cfg.Policy.Daemon.Target(cfg.Policy)
	d.mu.Unlock()
	d.logf("config reloaded from %q", src)
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
	fmt.Fprintf(d.deps.Log, "%s %s\n", d.deps.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
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
	d.retryConfig(now)
	cfg := d.config()
	p := cfg.Policy
	dp := p.Daemon
	degraded := d.Degraded()
	if degraded {
		dp.AutoAct = false // alert-only, whatever the default says
	}
	if d.recoveryDirty {
		_ = d.saveRecovery()
	}
	d.set(func(s *Status) {
		s.Ticks++
		s.LastTick = now
		s.NextTick = now.Add(dp.Interval())
	})

	du, err := d.deps.Disk(cfg.Volume)
	if err != nil {
		d.note(fmt.Sprintf("statfs %q: %v", cfg.Volume, err))
		return d.Snapshot()
	}
	label, _ := status.Of(p, du)
	free := du.FreeGB()

	st, err := state.Update(p.StateFile, func(s *state.State) {
		s.Volume = cfg.Volume
		s.Record("daemon", du, now)
	})
	if err != nil {
		// the state file is left as it is; the forecast works from this reading alone
		d.note("state: " + err.Error())
		st.Volume = cfg.Volume
		st.Record("daemon", du, now)
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

	// expired quarantine, as the hourly tick does, at most hourly
	stopping := d.env.Ctx != nil && d.env.Ctx.Err() != nil
	if !degraded && !stopping {
		d.purge(cfg, now)
	}

	// act only when told to, only under critical, only after the cooldown,
	// and never once shutdown has begun
	last := d.recovery.LastAct
	// A last_act in the future (the clock moved back) does not block.
	if dp.AutoAct && !stopping && !d.recovery.Paused && label == "CRITICAL" && (last.IsZero() || last.After(now) || now.Sub(last) >= dp.Cooldown()) && d.startAct(now) {
		target := dp.Target(p)
		d.busy("acting: ensure " + fmt.Sprintf("%.0f GB", target))
		res, err := d.deps.Ensure(cfg, d.env, target, true, now)
		busyLock := lockBusy(err)
		if busyLock {
			// another oos held the mutation lock: nothing was tried, so the
			// cooldown does not start and the brake does not count it
			d.recovery.LastAct = last
		}
		d.recovery.Observe(res, err, dp.RecoveryFailureLimit)
		_ = d.saveRecovery()
		d.busy("")
		if !busyLock {
			// a busy lock did nothing: keep the last real result in status
			d.set(func(s *Status) { s.AutoAct.LastAt = now; s.AutoAct.Last = &res })
		}
		switch {
		case busyLock:
			d.logf("auto-act skipped: %v", err)
		case refusedRun(err):
			d.note("auto-act refused: " + err.Error())
			if !d.refusedOnce {
				d.refusedOnce = true
				d.alert(dp, "auto-act-refused", "oos: cleanup refused by the filesystem", err.Error(), now, true)
			}
		case err != nil && !partialRun(err):
			d.note("auto-act: " + err.Error())
			d.alert(dp, "auto-act", "oos: could not act", err.Error(), now, true)
		default:
			if err != nil {
				d.note("auto-act partial: " + err.Error())
			}
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

	// periodic sized refresh: known entries and docker, into the state file;
	// the interval is jittered so a fleet restarted together spreads out
	if every := dp.SizedEvery(); every > 0 && !now.Before(d.nextSized) {
		d.nextSized = now.Add(d.jittered(every))
		d.busy("sizing known entries")
		d.sized(cfg, now)
		d.busy("")
	}
	return d.Snapshot()
}

// jittered spreads a period by +/-10%.
func (d *Daemon) jittered(every time.Duration) time.Duration {
	spread := every / 5
	return every - every/10 + d.deps.Jitter(spread)
}

// purge runs the scheduled quarantine purge: only when the policy asks, at
// most once per purgeEvery (the time is persisted, so restarts do not bring
// it back every tick), and not at all when no store holds a batch.
func (d *Daemon) purge(cfg *config.Config, now time.Time) {
	p := cfg.Policy
	if !p.Quarantine || !p.AgentPurgeExpired || d.recovery.Paused {
		return
	}
	lp := d.recovery.LastPurge
	if !lp.IsZero() && !lp.After(now) && now.Sub(lp) < purgeEvery {
		return
	}
	n, err := d.deps.Batches(cfg)
	if err == nil && n == 0 {
		return
	}
	d.recovery.LastPurge = now
	d.set(func(s *Status) { s.LastPurge = now })
	_ = d.saveRecovery()
	d.busy("purging expired quarantine")
	freed, names, perr := d.deps.Purge(d.env.Ctx, cfg, now)
	d.busy("")
	if len(names) > 0 || perr != nil {
		d.logf("purge expired quarantine: freed=%s batches=%q err=%v", size.Human(freed), names, perr)
	}
	if perr != nil && !lockBusy(perr) {
		d.note("purge: " + perr.Error())
	}
}

// startAct records the action time before acting so the cooldown holds.
// A failed write does not stop the action or pause the brake: the cooldown
// is kept in memory, the error is reported, and the write is retried next
// tick. (The usual cause is a full disk, which is when acting matters.)
func (d *Daemon) startAct(now time.Time) bool {
	d.recovery.LastAct = now
	_ = d.saveRecovery()
	if d.recovery.Paused {
		d.note("auto-act: " + d.recovery.Reason)
		return false
	}
	return true
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
	if _, err := state.Update(cfg.Policy.StateFile, func(st *state.State) {
		for k, v := range sum.Known {
			st.Known[k] = v
		}
	}); err != nil {
		d.note("sized state: " + err.Error())
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

// Run ticks until ctx ends. The first tick comes after a random offset in
// [0, interval) and each later wait is jittered by +/-10%, so a fleet
// restarted together by a rolling upgrade does not tick in lockstep. ctx
// also reaches the ensure path through env.Ctx, so a SIGTERM stops a
// running cleanup at its next safe point instead of being SIGKILLed.
func (d *Daemon) Run(ctx context.Context) {
	d.env.Ctx = ctx
	cfg := d.config()
	d.logf("oos daemon %s started on %q, interval %s, socket %q, auto_act=%v, degraded=%v", d.version, cfg.Volume, cfg.Policy.Daemon.Interval(), cfg.Policy.Daemon.SocketPath(d.env.Home), cfg.Policy.Daemon.AutoAct, d.Degraded())
	first := d.deps.Jitter(cfg.Policy.Daemon.Interval())
	d.set(func(s *Status) { s.NextTick = d.deps.Now().Add(first) })
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			d.logf("stopping: %v", ctx.Err())
			return
		case <-d.reload:
			// reloads run here, between ticks, so they never race a tick
			_ = d.TryReload()
		case <-timer.C:
			d.safeTick()
			wait := d.jittered(d.config().Policy.Daemon.Interval())
			d.set(func(s *Status) { s.NextTick = d.deps.Now().Add(wait) })
			timer.Reset(wait)
		}
	}
}

// RequestReload asks Run to re-read the config between ticks (SIGHUP).
func (d *Daemon) RequestReload() {
	select {
	case d.reload <- struct{}{}:
	default: // one is already pending
	}
}

// safeTick runs one tick; a panic is recorded as an error, not a crash, so
// one bad tick cannot put the service manager into a restart loop.
func (d *Daemon) safeTick() {
	defer func() {
		if r := recover(); r != nil {
			d.busy("")
			d.note(fmt.Sprintf("tick panic: %v", r))
		}
	}()
	d.Tick(d.deps.Now())
}
