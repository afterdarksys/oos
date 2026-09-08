package repos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afterdarksys/oos/internal/testutil"
)

func TestBuildDirKind(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, filepath.Join(home, "rs", "Cargo.toml"), 1)
	testutil.Write(t, filepath.Join(home, "js", "package.json"), 1)
	testutil.Write(t, filepath.Join(home, "py", ".venv", "pyvenv.cfg"), 1)
	testutil.Write(t, filepath.Join(home, "plain", "x"), 1)
	cases := []struct {
		parent, name, kind string
	}{
		{"rs", "target", "Rust target"},
		{"js", "node_modules", "node_modules"},
		{"js", ".next", ".next cache"},
		{"js", "dist", "dist output"},
		{"py", ".venv", "Python venv"},
		{"plain", "target", ""},       // no Cargo.toml beside it
		{"plain", "node_modules", ""}, // no package.json
		{"plain", ".venv", ""},        // no pyvenv.cfg inside
		{"plain", ".cache", "cache"},
		{"plain", "__pycache__", "Python cache"},
		{"plain", "src", ""},
	}
	for _, c := range cases {
		kind, _ := BuildDirKind(filepath.Join(home, c.parent), c.name)
		if kind != c.kind {
			t.Errorf("%s/%s: got %q want %q", c.parent, c.name, kind, c.kind)
		}
	}
}

func TestFindReposDepthAndSkips(t *testing.T) {
	home := t.TempDir()
	mk := func(rel string) {
		if err := os.MkdirAll(filepath.Join(home, rel, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("a")
	mk("b/c/d/e")     // depth 4
	mk("b/c/d/e/f/g") // depth 6, beyond default
	mk("a/node_modules/pkg")
	testutil.Write(t, filepath.Join(home, "a", "package.json"), 1)
	mk(".hidden/repo")
	repos, err := Find(home, 4)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(repos, "\n")
	if !strings.Contains(got, filepath.Join(home, "a")) || !strings.Contains(got, filepath.Join(home, "b/c/d/e")) {
		t.Errorf("missing repos: %s", got)
	}
	if strings.Contains(got, "f/g") || strings.Contains(got, "node_modules") || strings.Contains(got, ".hidden") {
		t.Errorf("should not descend past depth, into build dirs or hidden dirs: %s", got)
	}
}
