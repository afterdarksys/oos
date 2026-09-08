package testutil

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
)

// PolicyFor returns a permissive-enough policy rooted at home for tests.
// Docker is off: tests never talk to a daemon, and a wedged Docker Desktop
// once cost 120 s per check.
func PolicyFor(home string) config.Policy {
	off := false // no daemon, no tmutil: tests stay hermetic and fast
	return config.Policy{
		Docker:    &off,
		Snapshots: &off,
		MinFreeGB: 1, WarnFreeGB: 2, RequireYes: true, MaxDeleteGBPerRun: 1,
		AllowOutsideHome: false, AllowCommands: false,
		NeverTouch:   []string{filepath.Join(home, "keep")},
		MinPathDepth: 3,
		LogFile:      filepath.Join(home, "log"), StateFile: filepath.Join(home, "state.json"),
		BigFileMinMB: 1, ScanTopN: 10,
	}
}

// Write creates p with n bytes of filler, making parents as needed.
func Write(t *testing.T, p string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Age sets p's mtime d into the past.
func Age(t *testing.T, p string, d time.Duration) {
	t.Helper()
	when := time.Now().Add(-d)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

// NoProcs is a process lister that sees nothing.
func NoProcs() ([]string, error) { return nil, nil }
