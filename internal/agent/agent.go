package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
)

const defaultAlertDropGB = 10

// Tick is what the hourly job runs. It is deliberately cheap: one
// statfs, one state read and write, no sizing. It notifies when free space
// is under the warn line, and when free space fell by more than
// alert_drop_gb since the previous tick (within three hours, so a reboot
// after a week away does not fire a stale alert). With agent_purge_expired
// it also releases quarantine batches older than quarantine_days.
func Tick(cfg *config.Config, jsonOut bool, now time.Time, out, errw io.Writer) int {
	du, err := size.Disk(cfg.Volume)
	if err != nil {
		fmt.Fprintf(errw, "oos: statfs %s: %v\n", cfg.Volume, err)
		return status.ExitUsage
	}
	label, code := status.Of(cfg.Policy, du)
	st, _ := state.Load(cfg.Policy.StateFile)
	var prev *state.HistoryPoint
	for i := len(st.History) - 1; i >= 0; i-- {
		if st.History[i].Event == "agent" {
			prev = &st.History[i]
			break
		}
	}
	var msgs []string
	if code != status.ExitOK {
		msgs = append(msgs, fmt.Sprintf("%.1f GB free of %.1f GB (%s)", du.FreeGB(), du.TotalGB(), label))
	}
	threshold := cfg.Policy.AlertDropGB
	if threshold <= 0 {
		threshold = defaultAlertDropGB
	}
	var drop float64
	if prev != nil && now.Sub(prev.At) <= 3*time.Hour {
		drop = prev.FreeGB - du.FreeGB()
		if drop >= threshold {
			msgs = append(msgs, fmt.Sprintf("dropped %.1f GB in %s; run oos -d to see what grew", drop, now.Sub(prev.At).Round(time.Minute)))
		}
	}
	var purgedNames []string
	var purgedBytes int64
	if cfg.Policy.Quarantine && cfg.Policy.AgentPurgeExpired {
		olderThan := time.Duration(cfg.Policy.QuarantineDays) * 24 * time.Hour
		freed, names, perr := plan.PurgeBatches(cfg.Policy.QuarantineDir, olderThan, now, false)
		purgedNames, purgedBytes = names, freed
		if len(names) > 0 || perr != nil {
			if logf, lerr := state.OpenLog(cfg.Policy.LogFile); lerr == nil {
				fmt.Fprintf(logf, "%s agent purge freed=%d batches=%s err=%v\n", now.UTC().Format(time.RFC3339), freed, strings.Join(names, ","), perr)
				logf.Close()
			}
		}
		if perr != nil {
			fmt.Fprintf(errw, "oos: agent purge: %v\n", perr)
		}
		if len(names) > 0 {
			// re-read after the purge so the recorded reading reflects it
			if du2, err := size.Disk(cfg.Volume); err == nil {
				du = du2
			}
		}
	}
	st.Volume = cfg.Volume
	st.Record("agent", du, now)
	if err := state.Save(cfg.Policy.StateFile, st); err != nil {
		fmt.Fprintf(errw, "oos: save state: %v\n", err)
	}
	// rate alert: where the last few hours of readings lead
	fc := st.Forecast(now, cfg.Policy.ForecastWindow(), cfg.Policy.WarnFreeGB, cfg.Policy.MinFreeGB)
	if h := cfg.Policy.AlertHours(); h > 0 && fc.HoursToCritical > 0 && fc.HoursToCritical <= h {
		msgs = append(msgs, fmt.Sprintf("critical in %.1fh at %+.2f GB/h; run oos -d to see what grew", fc.HoursToCritical, fc.RateGBPerHour))
	}
	notified := false
	if len(msgs) > 0 {
		if err := Notify("oos: disk "+label, strings.Join(msgs, "; ")); err != nil {
			fmt.Fprintf(errw, "oos: notify: %v\n", err)
		} else {
			notified = true
		}
	}
	if jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{
			"free_gb": du.FreeGB(), "status": label, "drop_gb": drop, "notified": notified,
			"alerts": msgs, "purged_batches": purgedNames, "purged_bytes": purgedBytes, "forecast": fc,
		})
		return code
	}
	fmt.Fprintf(out, "%s agent %s free=%.1fGB", now.Format("2006-01-02 15:04"), strings.ToLower(label), du.FreeGB())
	if fc.Falling() {
		fmt.Fprintf(out, " rate=%+.2fGB/h", fc.RateGBPerHour)
		if fc.HoursToCritical > 0 {
			fmt.Fprintf(out, " critical-in=%.1fh", fc.HoursToCritical)
		}
	}
	if prev != nil {
		fmt.Fprintf(out, " drop=%.1fGB", drop)
	}
	if len(purgedNames) > 0 {
		fmt.Fprintf(out, " purged=%d(%s)", len(purgedNames), size.Human(purgedBytes))
	}
	if notified {
		fmt.Fprintf(out, " notified")
	}
	fmt.Fprintln(out)
	return code
}

// Notify posts an alert; tests replace it. Exec runs commands for the installer.
var (
	Notify = notify
	Exec   = Runner(ExecRun)
)

type Runner func(name string, args ...string) error

func ExecRun(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
