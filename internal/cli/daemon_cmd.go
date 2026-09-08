package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/afterdarksys/oos/internal/agent"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/daemon"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
)

// doDaemon runs the resident watcher in the foreground until SIGINT or
// SIGTERM; SIGHUP reloads the config. launchd or systemd keep it alive.
func doDaemon(cfg *config.Config, src string, env guard.Env, o *opts, stdout, stderr io.Writer) int {
	logw := io.Writer(stdout)
	if lf, err := state.OpenLog(filepath.Join(filepath.Dir(cfg.Policy.LogFile), "daemon.log")); err == nil {
		defer lf.Close()
		logw = io.MultiWriter(stdout, lf)
	}
	d := daemon.New(cfg, src, env, Version, daemon.Deps{
		Disk: size.Disk, Now: time.Now, Notify: agent.Notify, OpenFiles: guard.OpenFilesByProcess, Log: logw,
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			ncfg, nsrc, err := config.Load(o.config, env.Home)
			if err != nil {
				fmt.Fprintf(logw, "reload failed, keeping the running config: %v\n", err)
				continue
			}
			d.Reload(ncfg, nsrc)
		}
	}()
	errc := make(chan error, 1)
	go func() { errc <- daemon.Serve(ctx, cfg.Policy.Daemon.SocketPath(env.Home), d) }()
	d.Run(ctx)
	if err := <-errc; err != nil {
		fmt.Fprintln(stderr, "oos: daemon:", err)
		return status.ExitUsage
	}
	return status.ExitOK
}

// doStatus asks the daemon; without one it reads the last tick from the
// state file and says so. Exit code is the disk status.
func doStatus(cfg *config.Config, env guard.Env, o *opts, out, errw io.Writer) int {
	sock := cfg.Policy.Daemon.SocketPath(env.Home)
	s, err := daemon.Query(sock, 3*time.Second)
	if err != nil {
		st, _ := state.Load(cfg.Policy.StateFile)
		du, derr := size.Disk(cfg.Volume)
		if derr != nil {
			fmt.Fprintf(errw, "oos: statfs %s: %v\n", cfg.Volume, derr)
			return status.ExitUsage
		}
		label, code := status.Of(cfg.Policy, du)
		fc := st.Forecast(time.Now(), cfg.Policy.ForecastWindow(), cfg.Policy.WarnFreeGB, cfg.Policy.MinFreeGB)
		if o.jsonOut {
			_ = json.NewEncoder(out).Encode(map[string]any{
				"daemon": false, "socket": sock, "error": err.Error(), "free_gb": du.FreeGB(), "total_gb": du.TotalGB(),
				"status": label, "forecast": fc, "last_state_update": st.UpdatedAt,
			})
			return code
		}
		fmt.Fprintf(out, "daemon: not running (%s: %v)\n", sock, err)
		fmt.Fprintf(out, "  now: %.1f GB free of %.1f GB (%s); %s\n", du.FreeGB(), du.TotalGB(), label, fc.String())
		if !st.UpdatedAt.IsZero() {
			fmt.Fprintf(out, "  last recorded reading: %s\n", st.UpdatedAt.Local().Format("2006-01-02 15:04"))
		}
		fmt.Fprintln(out, "  start it with: oos --daemon   (or --install-daemon to keep it running)")
		return code
	}
	code := status.ExitOK
	switch s.Label {
	case "WARN":
		code = status.ExitWarn
	case "CRITICAL":
		code = status.ExitCritical
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(s)
		return code
	}
	fmt.Fprintf(out, "daemon: oos %s pid %d, up %s, %d ticks every %s, config %s\n", s.Version, s.PID, time.Since(s.Started).Round(time.Minute), s.Ticks, s.Interval, s.Config)
	fmt.Fprintf(out, "  %s: %.1f GB free of %.1f GB on %s; last tick %s, next %s\n", strings.ToLower(s.Label), s.FreeGB, s.TotalGB, s.Volume,
		s.LastTick.Local().Format("15:04:05"), s.NextTick.Local().Format("15:04:05"))
	fmt.Fprintf(out, "  %s\n", s.Forecast.String())
	if s.Busy != "" {
		fmt.Fprintf(out, "  busy: %s\n", s.Busy)
	}
	if s.DropGB > 0 {
		fmt.Fprintf(out, "  dropped %.1f GB since the previous tick\n", s.DropGB)
	}
	if len(s.Writers) > 0 {
		fmt.Fprintf(out, "  writing since %s:\n", s.WritersAt.Local().Format("15:04:05"))
		for _, w := range s.Writers {
			fmt.Fprintf(out, "    +%-9s %s (%s, pid %d)\n", size.Human(w.Delta), w.Path, w.Command, w.PID)
		}
	}
	if s.Sized != nil {
		fmt.Fprintf(out, "  sized %s: %s reclaimable by --cleanup", s.Sized.At.Local().Format("15:04"), size.Human(s.Sized.Reclaimable))
		if s.Sized.Docker != nil {
			fmt.Fprintf(out, "; docker %s unused images, %s build cache, %d dangling volumes", size.Human(s.Sized.Docker.Images.Reclaimable), size.Human(s.Sized.Docker.BuildCache.Reclaimable), len(s.Sized.Docker.Dangling))
		}
		fmt.Fprintln(out)
	}
	if s.Alerts > 0 {
		fmt.Fprintf(out, "  alerts: %d, last %s at %s\n", s.Alerts, s.LastAlert, s.LastAlertAt.Local().Format("15:04"))
	}
	act := "off"
	if s.AutoAct.Enabled {
		act = fmt.Sprintf("on, target %.0f GB", s.AutoAct.TargetGB)
		if s.AutoAct.Last != nil {
			act += fmt.Sprintf("; last %s: %.1f -> %.1f GB", s.AutoAct.LastAt.Local().Format("01-02 15:04"), s.AutoAct.Last.StartFreeGB, s.AutoAct.Last.FreeGB)
		}
	}
	fmt.Fprintf(out, "  auto-act: %s\n", act)
	for _, e := range s.Errors {
		fmt.Fprintf(out, "  error: %s\n", e)
	}
	return code
}

func doInstallDaemon(env guard.Env, o *opts, stdout, stderr io.Writer) int {
	if o.uninstallDaemon {
		if err := daemon.Uninstall(env.Home, o.system, agent.Exec); err != nil {
			fmt.Fprintln(stderr, "oos:", err)
			return status.ExitUsage
		}
		fmt.Fprintln(stdout, "daemon removed; --install-agent brings the hourly tick back")
		return status.ExitOK
	}
	exe, err := os.Executable()
	if err == nil {
		if r, e2 := filepath.EvalSymlinks(exe); e2 == nil {
			exe = r
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "oos: cannot resolve own path:", err)
		return status.ExitUsage
	}
	// the daemon records every tick itself; an hourly agent beside it would double-record
	_ = agent.Uninstall(env.Home, o.system, agent.Exec)
	if err := daemon.Install(env.Home, exe, o.system, agent.Exec); err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return status.ExitUsage
	}
	for p := range daemon.Files(env.Home, exe, o.system) {
		fmt.Fprintf(stdout, "wrote %s\n", p)
	}
	fmt.Fprintf(stdout, "daemon installed and started: %s --daemon (the hourly agent, if any, was removed)\n", exe)
	return status.ExitOK
}
