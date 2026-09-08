package leftovers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestScanPairsLibraryWithApps(t *testing.T) {
	home := t.TempDir()
	apps := filepath.Join(home, "apps")
	for _, a := range []string{"Docker.app", "Visual Studio Code.app", "GoLand.app"} {
		if err := os.MkdirAll(filepath.Join(apps, a, "Contents"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldDirs, oldID := AppDirs, BundleID
	AppDirs = []string{apps}
	BundleID = func(app string) string {
		return map[string]string{
			"Docker.app": "com.docker.docker", "Visual Studio Code.app": "com.microsoft.VSCode", "GoLand.app": "com.jetbrains.goland",
		}[filepath.Base(app)]
	}
	t.Cleanup(func() { AppDirs, BundleID = oldDirs, oldID })

	lib := filepath.Join(home, "Library")
	w := func(rel string) { testutil.Write(t, filepath.Join(lib, rel, "f"), 2<<20) }
	w("Application Support/Docker")                             // installed by name
	w("Application Support/com.gone.forever")                   // orphan by id
	w("Application Support/Firefox")                            // unmatched name
	w("Caches/com.microsoft.VSCode.ShipIt")                     // helper of an installed id
	w("Caches/com.apple.Safari")                                // Apple, never judged
	w("Containers/com.jetbrains.goland")                        // installed by id
	w("Saved Application State/com.old.app.savedState")         // orphan, suffix stripped
	w("Caches/Homebrew")                                        // known via config entry
	testutil.Write(t, filepath.Join(lib, "Caches", "tiny"), 10) // under the floor

	p := testutil.PolicyFor(home)
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home,
		KnownDirs: []config.Entry{{Path: filepath.Join(lib, "Caches", "Homebrew"), Type: "cache", Action: config.ActionRmContents}}}
	now := time.Now()
	found := InstalledApps(home)
	if len(found) != 3 || found[0].BundleID == "" {
		t.Fatalf("apps: %+v", found)
	}
	res, err := Scan(cfg, home, found, 1<<20, size.Filter{}, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Row{}
	for _, r := range res.Rows {
		got[r.Name] = r
	}
	want := map[string]string{
		"Docker": "installed", "com.gone.forever": "orphan", "Firefox": "unmatched", "com.microsoft.VSCode.ShipIt": "installed",
		"com.apple.Safari": "installed", "com.jetbrains.goland": "installed", "com.old.app.savedState": "orphan", "Homebrew": "known",
	}
	for name, v := range want {
		if got[name].Verdict != v {
			t.Errorf("%s: %q want %q (%s)", name, got[name].Verdict, v, got[name].Reason)
		}
	}
	if _, ok := got["tiny"]; ok {
		t.Error("entries under the floor are skipped")
	}
	if got["com.microsoft.VSCode.ShipIt"].App != "Visual Studio Code" || got["Docker"].App != "Docker" {
		t.Errorf("app names: %+v %+v", got["com.microsoft.VSCode.ShipIt"], got["Docker"])
	}
	if res.Rows[0].Verdict != "orphan" || res.ByVerdict["orphan"] < 4<<20 {
		t.Errorf("orphans first and totalled: %+v %v", res.Rows[0], res.ByVerdict)
	}
	line := AddLine(got["com.gone.forever"], home)
	if !strings.HasPrefix(line, `oos --add "~/Library/Application Support/com.gone.forever" --type leftover --action rm-contents`) {
		t.Errorf("add line: %s", line)
	}
	only, _ := Scan(cfg, home, found, 1<<20, size.Filter{Tag: "orphan"}, now)
	if len(only.Rows) != 2 {
		t.Errorf("--tag orphan: %d rows", len(only.Rows))
	}
	if _, err := Scan(cfg, filepath.Join(home, "nolib"), found, 1, size.Filter{}, now); err == nil {
		t.Error("no ~/Library must be an error")
	}
}
