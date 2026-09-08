// Package fleet asks every host in the policy's fleet list for its quick
// check over ssh and lines the answers up. Read-only: the remote command is
// oos -c -q -j, which sizes nothing and touches nothing.
package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
)

const DefaultTimeout = 20 * time.Second

// Host is one fleet member's answer.
type Host struct {
	Target          string         `json:"target"`
	Version         string         `json:"version,omitempty"`
	Volume          string         `json:"volume,omitempty"`
	FreeGB          float64        `json:"free_gb"`
	TotalGB         float64        `json:"total_gb"`
	Status          string         `json:"status"`
	QuarantineBytes int64          `json:"quarantine_bytes"`
	Forecast        state.Forecast `json:"forecast"`
	Elapsed         time.Duration  `json:"-"`
	ElapsedMS       int64          `json:"elapsed_ms"`
	Err             string         `json:"error,omitempty"`
}

// SSH runs oos on a remote target and returns its stdout. Tests replace it.
// The target is whatever ssh accepts: an alias, user@host, or a bare host.
var SSH = func(target string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	sshArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new", target, "oos"}
	sshArgs = append(sshArgs, args...)
	c := exec.CommandContext(ctx, "ssh", sshArgs...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		var ee *exec.ExitError
		// oos exits 1 (warn) or 2 (critical) with valid JSON on stdout: keep it
		if errors.As(err, &ee) && len(bytes.TrimSpace(out)) > 0 && (ee.ExitCode() == status.ExitWarn || ee.ExitCode() == status.ExitCritical) {
			return out, nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		return nil, errors.New(msg)
	}
	return out, nil
}

// checkJSON is the slice of oos -c -q -j this package reads.
type checkJSON struct {
	Version         string         `json:"version"`
	Volume          string         `json:"volume"`
	FreeGB          float64        `json:"free_gb"`
	TotalGB         float64        `json:"total_gb"`
	Status          string         `json:"status"`
	QuarantineBytes int64          `json:"quarantine_bytes"`
	Forecast        state.Forecast `json:"forecast"`
}

// parse reads one host's check output into a Host.
func parse(target string, out []byte) Host {
	h := Host{Target: target}
	var j checkJSON
	if err := json.Unmarshal(bytes.TrimSpace(out), &j); err != nil {
		h.Err = "unreadable answer: " + err.Error()
		h.Status = "?"
		return h
	}
	h.Version, h.Volume, h.FreeGB, h.TotalGB = j.Version, j.Volume, j.FreeGB, j.TotalGB
	h.Status, h.QuarantineBytes, h.Forecast = j.Status, j.QuarantineBytes, j.Forecast
	if h.Status == "" {
		h.Status = "?"
	}
	return h
}

// Collect asks every target in parallel. Order of the result follows the
// input; an unreachable host is a row with Err, never a missing row.
func Collect(targets []string, timeout time.Duration) []Host {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	hosts := make([]Host, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			out, err := SSH(t, timeout, "-c", "-q", "-j")
			if err != nil {
				hosts[i] = Host{Target: t, Status: "?", Err: err.Error()}
			} else {
				hosts[i] = parse(t, out)
			}
			hosts[i].Elapsed = time.Since(start)
			hosts[i].ElapsedMS = hosts[i].Elapsed.Milliseconds()
		}(i, t)
	}
	wg.Wait()
	return hosts
}

// Worst is the exit code the whole table deserves: an unreachable host is
// usage-level (3), otherwise the worst disk status.
func Worst(hosts []Host) int {
	code := status.ExitOK
	for _, h := range hosts {
		c := status.ExitOK
		switch {
		case h.Err != "":
			c = status.ExitUsage
		case h.Status == "CRITICAL":
			c = status.ExitCritical
		case h.Status == "WARN":
			c = status.ExitWarn
		}
		if c > code {
			code = c
		}
	}
	return code
}

// Report prints the table, least free space first, unreachable hosts last.
func Report(out io.Writer, hosts []Host, verbose bool) {
	rows := append([]Host{}, hosts...)
	sort.SliceStable(rows, func(i, j int) bool {
		if (rows[i].Err != "") != (rows[j].Err != "") {
			return rows[i].Err == ""
		}
		return rows[i].FreeGB < rows[j].FreeGB
	})
	fmt.Fprintf(out, "  %-8s %9s %9s %5s  %-9s %-28s %s\n", "status", "free", "total", "used", "quarantine", "forecast", "host")
	for _, h := range rows {
		if h.Err != "" {
			fmt.Fprintf(out, "  %-8s %9s %9s %5s  %-9s %-28s %s  (%s)\n", "?", "-", "-", "-", "-", "-", h.Target, h.Err)
			continue
		}
		used := "-"
		if h.TotalGB > 0 {
			used = fmt.Sprintf("%.0f%%", 100*(1-h.FreeGB/h.TotalGB))
		}
		fc := "-"
		switch {
		case h.Forecast.HoursToCritical > 0:
			fc = fmt.Sprintf("critical in %s", hours(h.Forecast.HoursToCritical))
		case h.Forecast.HoursToWarn > 0:
			fc = fmt.Sprintf("warn in %s", hours(h.Forecast.HoursToWarn))
		case h.Forecast.Note == "" && h.Forecast.RateGBPerHour != 0:
			fc = fmt.Sprintf("%+.2f GB/h", h.Forecast.RateGBPerHour)
		case h.Forecast.Note == "":
			fc = "flat"
		}
		q := "-"
		if h.QuarantineBytes > 0 {
			q = size.Human(h.QuarantineBytes)
		}
		fmt.Fprintf(out, "  %-8s %9.1f %9.1f %5s  %-9s %-28s %s\n", strings.ToLower(h.Status), h.FreeGB, h.TotalGB, used, q, fc, h.Target)
		if verbose {
			fmt.Fprintf(out, "           oos %s on %s, answered in %s; %s\n", h.Version, h.Volume, h.Elapsed.Round(10*time.Millisecond), h.Forecast.String())
		}
	}
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
