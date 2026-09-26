//go:build unix

package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/protect"
)

func TestCandidatesNeverSearchWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	for _, c := range Candidates(home) {
		if !filepath.IsAbs(c) {
			t.Fatalf("relative candidate %q would be read from the working directory", c)
		}
	}
	// A config in the working directory that would fail to parse must be
	// ignored entirely: Load falls through to the embedded default.
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "oos.json"), []byte(`{"version": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	if _, src, err := Load("", home); err != nil || src != "embedded default" {
		t.Fatalf("cwd oos.json was consulted: src=%q err=%v", src, err)
	}
}

func TestLoadRefusesWritableByOthers(t *testing.T) {
	home := t.TempDir()
	for _, mode := range []os.FileMode{0o620, 0o602, 0o666} {
		p := filepath.Join(t.TempDir(), "oos.json")
		if err := os.WriteFile(p, DefaultFor("darwin"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(p, home); err == nil || !strings.Contains(err.Error(), "writable by group or others") {
			t.Errorf("mode %04o: explicit config must be refused, got %v", mode, err)
		}
	}
	// The default location is held to the same rule.
	def := Candidates(home)[0]
	if err := os.MkdirAll(filepath.Dir(def), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(def, DefaultFor("darwin"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, src, err := Load("", home); err != nil || src != def {
		t.Fatalf("owner-only default config must load: %q %v", src, err)
	}
	if err := os.Chmod(def, 0o664); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load("", home); err == nil {
		t.Fatal("group-writable default config must be refused, not skipped")
	}
}

type fakeInfo struct {
	mode os.FileMode
	sys  any
}

func (f fakeInfo) Name() string       { return "oos.json" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return f.sys }

func TestCheckOwnerRefusesOtherUsers(t *testing.T) {
	other := &syscall.Stat_t{Uid: uint32(os.Geteuid() + 1)}
	if err := checkOwner("x", fakeInfo{mode: 0o600, sys: other}); err == nil || !strings.Contains(err.Error(), "owned by uid") {
		t.Fatalf("a config owned by another uid must be refused: %v", err)
	}
	if err := checkOwner("x", fakeInfo{mode: 0o600, sys: nil}); err == nil {
		t.Fatal("unknown owner must fail closed")
	}
	mine := &syscall.Stat_t{Uid: uint32(os.Geteuid())}
	if err := checkOwner("x", fakeInfo{mode: 0o644, sys: mine}); err != nil {
		t.Fatalf("owner-writable, world-readable is fine: %v", err)
	}
}

func TestDefaultsDisallowCommands(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		cfg, err := Parse(DefaultFor(goos), "/Users/test")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Policy.AllowCommands {
			t.Errorf("%s default must ship with allow_commands false", goos)
		}
	}
}

// Each shipped default must still validate against its own platform's
// built-in list; an entry under a newly protected path breaks loading.
func TestDefaultsValidateAgainstPlatformBuiltins(t *testing.T) {
	old := protect.Builtin
	t.Cleanup(func() { protect.Builtin = old })
	for _, goos := range []string{"darwin", "linux"} {
		protect.Builtin = protect.BuiltinFor(goos)
		if _, err := Parse(DefaultFor(goos), "/home/test"); err != nil {
			t.Errorf("%s default invalid under its built-ins: %v", goos, err)
		}
	}
}
