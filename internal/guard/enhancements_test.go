package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/testutil"
)

func staleFixture(t *testing.T) (home, dir string) {
	t.Helper()
	home = t.TempDir()
	dir = filepath.Join(home, "a", "archive")
	testutil.Write(t, filepath.Join(dir, "oldunref", "f"), 4096)
	testutil.Age(t, filepath.Join(dir, "oldunref"), 48*time.Hour)
	testutil.Write(t, filepath.Join(dir, "oldref", "bin", "python"), 4096)
	testutil.Age(t, filepath.Join(dir, "oldref"), 48*time.Hour)
	testutil.Write(t, filepath.Join(dir, "fresh", "f"), 4096)
	testutil.Write(t, filepath.Join(dir, "cwdref", "f"), 4096)
	testutil.Age(t, filepath.Join(dir, "cwdref"), 48*time.Hour)
	testutil.Write(t, filepath.Join(dir, ".lock"), 1)
	testutil.Age(t, filepath.Join(dir, ".lock"), 48*time.Hour)
	if err := os.Symlink(filepath.Join(home, "a"), filepath.Join(dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	return home, dir
}

func TestReferencedMatchesWholeComponents(t *testing.T) {
	refs := []string{"/usr/bin/python /x/archive/abc/bin/python serve", "cwd:/x/archive/def"}
	if !Referenced("/x/archive/abc", refs) {
		t.Error("cmdline reference missed")
	}
	if !Referenced("/x/archive/def", refs) {
		t.Error("cwd reference missed")
	}
	if Referenced("/x/archive/ab", refs) {
		t.Error("prefix must not match a longer component")
	}
	if Referenced("/x/archive/zzz", refs) {
		t.Error("unreferenced path matched")
	}
}

func TestClassifyChildren(t *testing.T) {
	_, dir := staleFixture(t)
	refs := []string{
		"/usr/local/bin/uv tool uvx " + filepath.Join(dir, "oldref", "bin", "python") + " -m server",
		filepath.Join(dir, "cwdref"),
	}
	kids, err := ClassifyChildren(dir, 24*time.Hour, refs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, k := range kids {
		got[filepath.Base(k.Path)] = k.Keep
	}
	if got["oldunref"] != "" {
		t.Errorf("oldunref should be a delete candidate, got keep=%q", got["oldunref"])
	}
	for name, want := range map[string]string{"oldref": "referenced", "cwdref": "referenced", "fresh": "floor", ".lock": "lock", "lnk": "symlink"} {
		if !strings.Contains(got[name], want) {
			t.Errorf("%s: keep=%q, want it to mention %q", name, got[name], want)
		}
	}
}
