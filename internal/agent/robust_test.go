package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestJitterDelayIsBoundedAndOptIn(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	full := func(d time.Duration) time.Duration { return d - 1 }
	if d := JitterDelay(get, full); d != 0 {
		t.Fatalf("no env, no delay: %s", d)
	}
	env[JitterEnv] = "600"
	if d := JitterDelay(get, full); d != 600*time.Second-1 {
		t.Fatalf("600s window: %s", d)
	}
	env[JitterEnv] = "99999"
	if d := JitterDelay(get, full); d >= MaxJitter {
		t.Fatalf("must be capped at %s: %s", MaxJitter, d)
	}
	env[JitterEnv] = "-5"
	if d := JitterDelay(get, full); d != 0 {
		t.Fatalf("nonsense is no delay: %s", d)
	}
	env[JitterEnv] = "600"
	if d := JitterDelay(get, nil); d < 0 || d >= 600*time.Second {
		t.Fatalf("real random out of range: %s", d)
	}
}

func TestSleepIsCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	if Sleep(ctx, time.Hour) {
		t.Fatal("a cancelled sleep must report it did not finish")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancel did not interrupt the sleep")
	}
	if !Sleep(context.Background(), time.Millisecond) {
		t.Fatal("a short sleep completes")
	}
}

func TestConfigRejectedNotifiesOncePerDayAndFails(t *testing.T) {
	var notes []string
	old := Notify
	Notify = func(title, msg string) error { notes = append(notes, msg); return nil }
	defer func() { Notify = old }()
	marker := filepath.Join(t.TempDir(), "state.json.config-rejected")
	now := time.Now()
	var out, errw bytes.Buffer
	cerr := errors.New(`parse config: unknown key "future"`)
	if code := ConfigRejected(cerr, marker, true, now, &out, &errw); code != status.ExitUsage {
		t.Fatalf("a rejected config must exit non-zero, got %d", code)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["kind"] != "agent-tick" || !strings.Contains(doc["config_error"].(string), "future") || doc["notified"] != true {
		t.Fatalf("json: %s %v", out.String(), err)
	}
	ConfigRejected(cerr, marker, false, now.Add(time.Hour), &out, &errw)
	if len(notes) != 1 {
		t.Fatalf("second tick within 24h must not notify again: %v", notes)
	}
	ConfigRejected(cerr, marker, false, now.Add(25*time.Hour), &out, &errw)
	if len(notes) != 2 {
		t.Fatalf("after 24h it notifies again: %v", notes)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("marker must exist")
	}
}

func TestAgentPlistSetsJitter(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd only")
	}
	for _, c := range Files(t.TempDir(), "/usr/local/bin/oos") {
		if !strings.Contains(c, "<key>"+JitterEnv+"</key><string>600</string>") {
			t.Errorf("plist lacks the jitter env:\n%s", c)
		}
	}
}

func TestTickJSONCarriesPurgeError(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	p.MinFreeGB, p.WarnFreeGB = 0.001, 0.002
	p.StateFile = filepath.Join(home, "state.json")
	p.Quarantine, p.AgentPurgeExpired = true, true
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	old := Notify
	Notify = func(string, string) error { return nil }
	defer func() { Notify = old }()
	l, err := plan.Mutation(cfg) // another oos holds the lock: the purge fails
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var out, errw bytes.Buffer
	Tick(cfg, true, time.Now(), &out, &errw)
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if pe, _ := doc["purge_error"].(string); pe == "" || doc["kind"] != "agent-tick" {
		t.Fatalf("purge_error missing from %s", out.String())
	}
}
