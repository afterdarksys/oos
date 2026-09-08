package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const defaultAlertDropGB = 10

// doAgentTick is what the hourly job runs. It is deliberately cheap: one
// statfs, one state read and write, no sizing. It notifies when free space
// is under the warn line, and when free space fell by more than
// alert_drop_gb since the previous tick (within three hours, so a reboot
// after a week away does not fire a stale alert). With agent_purge_expired
// it also releases quarantine batches older than quarantine_days.
func doAgentTick(cfg *Config, o *opts, now time.Time, out, errw io.Writer) int {
	du, err := diskUsage(cfg.Volume)
	if err != nil {
		fmt.Fprintf(errw, "oos: statfs %s: %v\n", cfg.Volume, err)
		return exitUsage
	}
	label, code := status(cfg.Policy, du)
	st, _ := loadState(cfg.Policy.StateFile)
	var prev *HistoryPoint
	for i := len(st.History) - 1; i >= 0; i-- {
		if st.History[i].Event == "agent" {
			prev = &st.History[i]
			break
		}
	}
	var msgs []string
	if code != exitOK {
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
		freed, names, perr := purgeBatches(cfg.Policy.QuarantineDir, olderThan, now, false)
		purgedNames, purgedBytes = names, freed
		if len(names) > 0 || perr != nil {
			if logf, lerr := openLog(cfg.Policy.LogFile); lerr == nil {
				fmt.Fprintf(logf, "%s agent purge freed=%d batches=%s err=%v\n", now.UTC().Format(time.RFC3339), freed, strings.Join(names, ","), perr)
				logf.Close()
			}
		}
		if perr != nil {
			fmt.Fprintf(errw, "oos: agent purge: %v\n", perr)
		}
		if len(names) > 0 {
			// re-read after the purge so the recorded reading reflects it
			if du2, err := diskUsage(cfg.Volume); err == nil {
				du = du2
			}
		}
	}
	st.Volume = cfg.Volume
	st.record("agent", du, now)
	if err := saveState(cfg.Policy.StateFile, st); err != nil {
		fmt.Fprintf(errw, "oos: save state: %v\n", err)
	}
	notified := false
	if len(msgs) > 0 {
		if err := notifyFn("oos: disk "+label, strings.Join(msgs, "; ")); err != nil {
			fmt.Fprintf(errw, "oos: notify: %v\n", err)
		} else {
			notified = true
		}
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{
			"free_gb": du.FreeGB(), "status": label, "drop_gb": drop, "notified": notified,
			"alerts": msgs, "purged_batches": purgedNames, "purged_bytes": purgedBytes,
		})
		return code
	}
	fmt.Fprintf(out, "%s agent %s free=%.1fGB", now.Format("2006-01-02 15:04"), strings.ToLower(label), du.FreeGB())
	if prev != nil {
		fmt.Fprintf(out, " drop=%.1fGB", drop)
	}
	if len(purgedNames) > 0 {
		fmt.Fprintf(out, " purged=%d(%s)", len(purgedNames), human(purgedBytes))
	}
	if notified {
		fmt.Fprintf(out, " notified")
	}
	fmt.Fprintln(out)
	return code
}
