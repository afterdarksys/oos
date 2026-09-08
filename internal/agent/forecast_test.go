package agent

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/testutil"
)

// TestTickForecastAlert: five hourly readings falling 2 GB/h toward the
// critical line make the tick say so; the same fall with the alert horizon
// disabled stays quiet.
func TestTickForecastAlert(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	p.MinFreeGB = 0.001 // the temp volume has plenty of room; the fit, not the level, must fire
	p.WarnFreeGB = 0.002
	p.StateFile = filepath.Join(home, "state.json")
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	now := time.Now()

	// seed a history whose slope hits MinFreeGB within 24h: use the real free
	// GB of the volume as the endpoint so the tick's own reading fits the line
	var notes []string
	old := Notify
	Notify = func(title, msg string) error { notes = append(notes, title+": "+msg); return nil }
	t.Cleanup(func() { Notify = old })

	var out, errw bytes.Buffer
	Tick(cfg, false, now, &out, &errw) // first tick records the real free space
	st, _ := state.Load(p.StateFile)
	free := st.History[len(st.History)-1].FreeGB
	st.History = nil
	for h := 5; h >= 1; h-- {
		st.History = append(st.History, state.HistoryPoint{At: now.Add(-time.Duration(h) * time.Hour), FreeGB: free + float64(h)*free/10, Event: "agent"})
	}
	if err := state.Save(p.StateFile, st); err != nil {
		t.Fatal(err)
	}
	// slope is free/10 per hour, so critical (~0) is ~10 h away: inside the default 24 h horizon
	notes = nil
	out.Reset()
	Tick(cfg, true, now, &out, &errw)
	var j struct {
		Alerts   []string       `json:"alerts"`
		Forecast state.Forecast `json:"forecast"`
	}
	if err := json.Unmarshal(out.Bytes(), &j); err != nil {
		t.Fatal(err, out.String())
	}
	if !j.Forecast.Falling() || j.Forecast.HoursToCritical < 8 || j.Forecast.HoursToCritical > 12 {
		t.Errorf("forecast: %+v", j.Forecast)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "critical in") {
		t.Errorf("expected one rate alert, got %v (alerts %v)", notes, j.Alerts)
	}

	// horizon disabled: same data, no alert
	cfg.Policy.AlertHoursToCritical = -1
	notes = nil
	out.Reset()
	Tick(cfg, false, now.Add(time.Minute), &out, &errw)
	if len(notes) != 0 {
		t.Errorf("alert horizon -1 must never alert on the rate: %v", notes)
	}
	if !strings.Contains(out.String(), "rate=") {
		t.Errorf("text tick still shows the rate:\n%s", out.String())
	}

	// horizon shorter than the projection: no alert either
	cfg.Policy.AlertHoursToCritical = 2
	notes = nil
	Tick(cfg, false, now.Add(2*time.Minute), &out, &errw)
	if len(notes) != 0 {
		t.Errorf("a 2 h horizon must not alert on a 10 h projection: %v", notes)
	}
}
