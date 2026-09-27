package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strconv"
	"time"

	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/status"
)

// JitterEnv is set by the launchd agent plist: launchd cannot randomize
// StartInterval, so a fleet of Macs would tick in the same second. The tick
// sleeps a random [0, value) seconds first, capped at MaxJitter.
const JitterEnv = "OOS_AGENT_JITTER_SECONDS"

// MaxJitter bounds the pre-tick sleep whatever the environment says.
const MaxJitter = 10 * time.Minute

// JitterDelay reads JitterEnv and returns the random delay to sleep, or 0.
func JitterDelay(getenv func(string) string, n func(time.Duration) time.Duration) time.Duration {
	v := getenv(JitterEnv)
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0
	}
	max := time.Duration(secs) * time.Second
	if max > MaxJitter {
		max = MaxJitter
	}
	if n == nil {
		n = func(d time.Duration) time.Duration { return rand.N(d) }
	}
	return n(max)
}

// Sleep waits d or until ctx ends; it reports whether the full wait passed.
func Sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// configNotifyEvery rate-limits the "config rejected" notification.
const configNotifyEvery = 24 * time.Hour

// ConfigRejected is the agent tick when the config does not load: nothing
// is measured or purged on a policy that cannot be read. It notifies at most
// once per 24h (marker is the file whose mtime records the last one) and
// exits with the usage code, so a scheduler or monitor sees it.
func ConfigRejected(cerr error, marker string, jsonOut bool, now time.Time, out, errw io.Writer) int {
	msg := "oos: config rejected: " + cerr.Error() + "; the scheduled check did not run"
	fmt.Fprintln(errw, msg)
	notified := false
	due := true
	if fi, err := os.Stat(marker); err == nil && now.Sub(fi.ModTime()) < configNotifyEvery && !fi.ModTime().After(now) {
		due = false
	}
	if due {
		if err := Notify("oos: config rejected", msg); err != nil {
			fmt.Fprintf(errw, "oos: notify: %v\n", err)
		} else {
			notified = true
			if marker != "" {
				if err := plan.WriteRecord(marker, []byte(now.UTC().Format(time.RFC3339)+"\n")); err == nil {
					_ = os.Chtimes(marker, now, now)
				}
			}
		}
	}
	if jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{
			"kind": "agent-tick", "config_error": cerr.Error(), "notified": notified,
			"error_kind": status.Kind(status.ExitUsage),
		})
	}
	return status.ExitUsage
}
