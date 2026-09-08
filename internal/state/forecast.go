package state

import (
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	// DefaultForecastWindow is how far back a rate is fitted.
	DefaultForecastWindow = 6 * time.Hour
	// minForecastSpan is the shortest window a rate is trusted over: two
	// readings a minute apart extrapolate to hundreds of GB/day of noise.
	minForecastSpan = time.Hour
	minForecastN    = 3
)

// Forecast is the free-space trend over a window and where it leads.
type Forecast struct {
	RateGBPerHour   float64 `json:"rate_gb_per_hour"` // negative means filling
	WindowHours     float64 `json:"window_hours"`     // span actually fitted
	Points          int     `json:"points"`
	FreeGB          float64 `json:"free_gb"`
	HoursToWarn     float64 `json:"hours_to_warn,omitempty"`     // 0: not falling, or already under
	HoursToCritical float64 `json:"hours_to_critical,omitempty"` // 0: not falling, or already under
	Note            string  `json:"note,omitempty"`              // why there is no rate
}

// Falling reports whether free space is going down at a rate the fit trusts.
func (f Forecast) Falling() bool { return f.Note == "" && f.RateGBPerHour < 0 }

// String is the one-line human form.
func (f Forecast) String() string {
	if f.Note != "" {
		return "forecast: " + f.Note
	}
	s := fmt.Sprintf("forecast: %+.2f GB/h over %s (%d readings)", f.RateGBPerHour, hours(f.WindowHours), f.Points)
	switch {
	case f.HoursToCritical > 0:
		s += fmt.Sprintf("; critical in %s", hours(f.HoursToCritical))
	case f.HoursToWarn > 0:
		s += fmt.Sprintf("; warn in %s", hours(f.HoursToWarn))
	case f.RateGBPerHour < 0:
		s += "; already under the line"
	}
	return s
}

func hours(h float64) string {
	switch {
	case h < 1:
		return fmt.Sprintf("%.0fm", h*60)
	case h < 48:
		return fmt.Sprintf("%.1fh", h)
	default:
		return fmt.Sprintf("%.1fd", h/24)
	}
}

// Forecast fits a least-squares line to the readings inside window and
// projects when free space crosses the warn and critical lines. It needs
// at least three readings spanning an hour; otherwise Note says what is
// missing and the rate is zero.
func (s *State) Forecast(now time.Time, window time.Duration, warnGB, minGB float64) Forecast {
	if window <= 0 {
		window = DefaultForecastWindow
	}
	pts := make([]HistoryPoint, 0, len(s.History))
	cutoff := now.Add(-window)
	for _, p := range s.History {
		if !p.At.Before(cutoff) && !p.At.After(now.Add(time.Minute)) {
			pts = append(pts, p)
		}
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].At.Before(pts[j].At) })
	f := Forecast{Points: len(pts)}
	if len(pts) > 0 {
		f.FreeGB = pts[len(pts)-1].FreeGB
	}
	if len(pts) < minForecastN {
		f.Note = fmt.Sprintf("needs %d readings inside %s, have %d", minForecastN, hours(window.Hours()), len(pts))
		return f
	}
	span := pts[len(pts)-1].At.Sub(pts[0].At)
	f.WindowHours = span.Hours()
	if span < minForecastSpan {
		f.Note = fmt.Sprintf("needs %s of readings, have %s", hours(minForecastSpan.Hours()), span.Round(time.Minute))
		return f
	}
	// least squares on (hours since first, free GB)
	t0 := pts[0].At
	var sx, sy, sxx, sxy float64
	for _, p := range pts {
		x := p.At.Sub(t0).Hours()
		y := p.FreeGB
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	n := float64(len(pts))
	den := n*sxx - sx*sx
	if den == 0 {
		f.Note = "readings share one timestamp"
		return f
	}
	slope := (n*sxy - sx*sy) / den
	if math.Abs(slope) < 1e-6 {
		slope = 0
	}
	f.RateGBPerHour = slope
	if slope < 0 {
		if f.FreeGB > warnGB {
			f.HoursToWarn = (f.FreeGB - warnGB) / -slope
		}
		if f.FreeGB > minGB {
			f.HoursToCritical = (f.FreeGB - minGB) / -slope
		}
	}
	return f
}
