package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
)

// doHistory prints the last N free-space readings: when, what oos was doing,
// free GB, and the change from the previous reading. The trend is the point.
func doHistory(cfg *config.Config, o *opts, out io.Writer) int {
	st, err := state.Load(cfg.Policy.StateFile)
	if err != nil || len(st.History) == 0 {
		fmt.Fprintln(out, "no readings yet")
		return status.ExitOK
	}
	h := st.History
	sort.SliceStable(h, func(i, j int) bool { return h[i].At.Before(h[j].At) })
	if len(h) > o.history {
		h = h[len(h)-o.history:]
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(h)
		return status.ExitOK
	}
	var prev *state.HistoryPoint
	for i := range h {
		p := h[i]
		delta := ""
		if prev != nil {
			delta = fmt.Sprintf("%+.1f GB over %s", p.FreeGB-prev.FreeGB, p.At.Sub(prev.At).Round(time.Minute))
		}
		fmt.Fprintf(out, "  %s  %-8s %7.1f GB  %s\n", p.At.Local().Format("2006-01-02 15:04"), p.Event, p.FreeGB, delta)
		prev = &h[i]
	}
	first, last := h[0], h[len(h)-1]
	span := last.At.Sub(first.At)
	switch {
	case len(h) < 2:
	case span < minTrendSpan:
		// two readings a minute apart extrapolate to hundreds of GB/day of noise
		fmt.Fprintf(out, "  trend: needs %s of readings, have %s\n", minTrendSpan, span.Round(time.Minute))
	default:
		perDay := (last.FreeGB - first.FreeGB) / (span.Hours() / 24)
		fmt.Fprintf(out, "  trend: %+.1f GB/day over %s\n", perDay, span.Round(time.Hour))
	}
	// the forecast uses the policy window over the whole history, not the N rows shown
	fc := st.Forecast(time.Now(), cfg.Policy.ForecastWindow(), cfg.Policy.WarnFreeGB, cfg.Policy.MinFreeGB)
	fmt.Fprintf(out, "  %s\n", fc.String())
	return status.ExitOK
}

// minTrendSpan is the shortest window a GB/day figure is printed for.
const minTrendSpan = time.Hour
