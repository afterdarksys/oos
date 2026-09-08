package state

import (
	"strings"
	"testing"
	"time"
)

func TestForecastFitsAndProjects(t *testing.T) {
	now := time.Now()
	s := &State{}
	// 2 GB/h fall over the last five hours, 40 GB free now
	for h := 5; h >= 0; h-- {
		s.History = append(s.History, HistoryPoint{At: now.Add(-time.Duration(h) * time.Hour), FreeGB: 40 + 2*float64(h), Event: "agent"})
	}
	// an old reading outside the window must not skew the fit
	s.History = append(s.History, HistoryPoint{At: now.Add(-30 * time.Hour), FreeGB: 500, Event: "agent"})
	f := s.Forecast(now, 6*time.Hour, 30, 10)
	if f.Note != "" || f.Points != 6 {
		t.Fatalf("forecast: %+v", f)
	}
	if f.RateGBPerHour > -1.99 || f.RateGBPerHour < -2.01 {
		t.Errorf("rate %.3f want -2", f.RateGBPerHour)
	}
	if f.HoursToWarn < 4.9 || f.HoursToWarn > 5.1 || f.HoursToCritical < 14.9 || f.HoursToCritical > 15.1 {
		t.Errorf("projection: warn %.2f critical %.2f", f.HoursToWarn, f.HoursToCritical)
	}
	if !f.Falling() || !strings.Contains(f.String(), "critical in 15.0h") {
		t.Errorf("string: %s", f.String())
	}

	// already under warn: only the critical projection remains
	g := s.Forecast(now, 6*time.Hour, 50, 10)
	if g.HoursToWarn != 0 || g.HoursToCritical == 0 {
		t.Errorf("under warn: %+v", g)
	}

	// rising or flat space never projects
	r := &State{}
	for h := 5; h >= 0; h-- {
		r.History = append(r.History, HistoryPoint{At: now.Add(-time.Duration(h) * time.Hour), FreeGB: 40 - float64(h)})
	}
	if f := r.Forecast(now, 6*time.Hour, 30, 10); f.Falling() || f.HoursToCritical != 0 || f.RateGBPerHour <= 0 {
		t.Errorf("rising: %+v", f)
	}
}

func TestForecastRefusesThinData(t *testing.T) {
	now := time.Now()
	s := &State{History: []HistoryPoint{{At: now.Add(-time.Minute), FreeGB: 50}, {At: now, FreeGB: 49}}}
	if f := s.Forecast(now, 6*time.Hour, 30, 10); f.Note == "" || f.RateGBPerHour != 0 || f.Falling() {
		t.Errorf("two readings must not forecast: %+v", f)
	}
	s.History = append(s.History, HistoryPoint{At: now.Add(-2 * time.Minute), FreeGB: 51})
	if f := s.Forecast(now, 6*time.Hour, 30, 10); !strings.Contains(f.Note, "needs 1.0h of readings") {
		t.Errorf("three readings inside two minutes must not forecast: %+v", f)
	}
	if f := (&State{}).Forecast(now, 0, 30, 10); !strings.Contains(f.Note, "needs 3 readings inside 6.0h") {
		t.Errorf("empty: %+v", f)
	}
}
