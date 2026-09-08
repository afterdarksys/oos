package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// local copies of testutil: testutil imports config, so config's own tests cannot.
func policyFor(home string) Policy {
	dockerOff := false
	return Policy{
		Docker:    &dockerOff,
		MinFreeGB: 1, WarnFreeGB: 2, RequireYes: true, MaxDeleteGBPerRun: 1,
		AllowOutsideHome: false, AllowCommands: false,
		NeverTouch:   []string{filepath.Join(home, "keep")},
		MinPathDepth: 3,
		LogFile:      filepath.Join(home, "log"), StateFile: filepath.Join(home, "state.json"),
		BigFileMinMB: 1, ScanTopN: 10,
	}
}

func write(t *testing.T, p string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func noProcs() ([]string, error) { return nil, nil }
