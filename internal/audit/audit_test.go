package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestAuditClassifiesHomeEntries(t *testing.T) {
	home := t.TempDir()
	known := filepath.Join(home, "known-cache")
	testutil.Write(t, filepath.Join(known, "f"), 65536)
	repo := filepath.Join(home, "project")
	testutil.Write(t, filepath.Join(repo, ".git", "HEAD"), 10)
	testutil.Write(t, filepath.Join(repo, "main.go"), 65536)
	stale := filepath.Join(home, ".oldtool")
	testutil.Write(t, filepath.Join(stale, "data"), 65536)
	testutil.Age(t, filepath.Join(stale, "data"), 400*24*time.Hour)
	testutil.Age(t, stale, 400*24*time.Hour)
	cachey := filepath.Join(home, "node_modules")
	testutil.Write(t, filepath.Join(cachey, "x"), 65536)
	protected := filepath.Join(home, "Documents")
	testutil.Write(t, filepath.Join(protected, "d"), 65536)
	tiny := filepath.Join(home, "tiny")
	testutil.Write(t, filepath.Join(tiny, "t"), 1)
	if err := os.Symlink(repo, filepath.Join(home, "lnk")); err != nil {
		t.Fatal(err)
	}

	p := testutil.PolicyFor(home)
	p.NeverTouch = []string{protected}
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home,
		KnownDirs: []config.Entry{{Path: known, Type: "cache", Action: config.ActionRmContents}}}
	rows, err := Scan(cfg, home, 32768, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Row{}
	for _, r := range rows {
		got[filepath.Base(r.Path)] = r
	}
	if _, ok := got["tiny"]; ok {
		t.Error("entries under the size floor must be omitted")
	}
	if _, ok := got["log"]; ok {
		// policyFor puts log/state under home; they are tiny or absent, fine either way
	}
	cases := map[string]struct{ status, hint string }{
		"known-cache":  {"known", "rm-contents"},
		"project":      {"unknown", "git repository"},
		".oldtool":     {"unknown", "nothing modified in"},
		"node_modules": {"unknown", "looks like a cache"},
		"Documents":    {"protected", "never_touch"},
	}
	for name, want := range cases {
		r, ok := got[name]
		if !ok {
			t.Errorf("%s missing from audit", name)
			continue
		}
		if r.Status != want.status || !strings.Contains(r.Suggestion, want.hint) {
			t.Errorf("%s: status=%q suggestion=%q, want %q containing %q", name, r.Status, r.Suggestion, want.status, want.hint)
		}
	}
	if !got[".oldtool"].Hidden {
		t.Error("dot entries must be flagged hidden")
	}
	// largest first
	for i := 1; i < len(rows); i++ {
		if rows[i].Bytes > rows[i-1].Bytes {
			t.Fatal("audit rows must be sorted by size descending")
		}
	}
}
