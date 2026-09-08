package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

// doHistory prints the last N free-space readings: when, what oos was doing,
// free GB, and the change from the previous reading. The trend is the point.
func doHistory(cfg *Config, o *opts, out io.Writer) int {
	st, err := loadState(cfg.Policy.StateFile)
	if err != nil || len(st.History) == 0 {
		fmt.Fprintln(out, "no readings yet")
		return exitOK
	}
	h := st.History
	sort.SliceStable(h, func(i, j int) bool { return h[i].At.Before(h[j].At) })
	if len(h) > o.history {
		h = h[len(h)-o.history:]
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(h)
		return exitOK
	}
	var prev *HistoryPoint
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
	if len(h) > 1 && last.At.After(first.At) {
		perDay := (last.FreeGB - first.FreeGB) / (last.At.Sub(first.At).Hours() / 24)
		fmt.Fprintf(out, "  trend: %+.1f GB/day over %s\n", perDay, last.At.Sub(first.At).Round(time.Hour))
	}
	return exitOK
}
