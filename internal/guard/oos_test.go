package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestGuardsRefuse(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	env := Env{Home: home, Procs: testutil.NoProcs}

	cacheDir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(cacheDir, "f"), 10)
	link := filepath.Join(home, "a", "link")
	if err := os.Symlink(cacheDir, link); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(home, "keep", "sub")
	testutil.Write(t, filepath.Join(keep, "f"), 10)
	outside := t.TempDir()

	cases := []struct {
		name string
		ent  config.Entry
		rule string
	}{
		{"never", config.Entry{Path: cacheDir, Action: config.ActionNever}, "action"},
		{"root", config.Entry{Path: "/", Action: config.ActionRmContents}, "root"},
		{"depth", config.Entry{Path: "/Users/x", Action: config.ActionRmContents}, "depth"},
		{"outside home", config.Entry{Path: outside, Action: config.ActionRmContents}, ""},
		{"never_touch", config.Entry{Path: keep, Action: config.ActionRmContents}, "never_touch"},
		{"symlink", config.Entry{Path: link, Action: config.ActionRmContents}, "symlink"},
		{"missing", config.Entry{Path: filepath.Join(home, "a", "nope"), Action: config.ActionRmContents}, "stat"},
		{"file with rm-contents", config.Entry{Path: filepath.Join(cacheDir, "f"), Action: config.ActionRmContents}, "kind"},
		{"dir with rm", config.Entry{Path: cacheDir, Action: config.ActionRm}, "kind"},
	}
	for _, c := range cases {
		err := env.CheckDeletable(p, c.ent)
		if err == nil {
			t.Errorf("%s: expected refusal", c.name)
			continue
		}
		if c.rule != "" && !strings.HasPrefix(err.Error(), c.rule+":") {
			t.Errorf("%s: expected rule %q, got %v", c.name, c.rule, err)
		}
	}
	// The happy path must pass so we know the refusals above are not vacuous.
	if err := env.CheckDeletable(p, config.Entry{Path: cacheDir, Action: config.ActionRmContents}); err != nil {
		t.Errorf("valid dir refused: %v", err)
	}
	// "/" in never_touch protects the root only; it must not swallow every path.
	p.NeverTouch = append(p.NeverTouch, "/")
	if err := env.CheckDeletable(p, config.Entry{Path: cacheDir, Action: config.ActionRmContents}); err != nil {
		t.Errorf("never_touch '/' must not refuse a home path: %v", err)
	}
	if err := env.CheckDeletable(p, config.Entry{Path: "/", Action: config.ActionRmContents}); err == nil {
		t.Error("root must still be refused")
	}
}

func TestGuardProcesses(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "uv")
	testutil.Write(t, filepath.Join(dir, "f"), 1)
	ent := config.Entry{Path: dir, Action: config.ActionRmContents, GuardProcesses: []string{"uvx"}}
	p := testutil.PolicyFor(home)

	busy := Env{Home: home, Procs: func() ([]string, error) { return []string{"/usr/local/bin/uv tool UVX serve"}, nil }}
	if err := busy.CheckDeletable(p, ent); err == nil || !strings.HasPrefix(err.Error(), "processes:") {
		t.Errorf("expected process refusal, got %v", err)
	}
	idle := Env{Home: home, Procs: func() ([]string, error) { return []string{"bash", "vim"}, nil }}
	if err := idle.CheckDeletable(p, ent); err != nil {
		t.Errorf("idle should pass: %v", err)
	}
	broken := Env{Home: home, Procs: func() ([]string, error) { return nil, os.ErrPermission }}
	if err := broken.CheckDeletable(p, ent); err == nil {
		t.Error("process lister failure must refuse, not pass")
	}
	none := Env{Home: home, Procs: nil}
	if err := none.CheckDeletable(p, ent); err == nil {
		t.Error("missing process lister with guards must refuse")
	}
}

// --- executor ---
