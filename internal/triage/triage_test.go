package triage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestStemAndMatchApp(t *testing.T) {
	cases := map[string]string{
		"ideaIU-2024.3.5.dmg":                             "ideaiu",
		"Visual Studio Code-darwin-arm64.zip":             "visualstudiocode",
		"OpenJDK21U-jdk_x64_mac_hotspot_21.0.4_7 (1).pkg": "openjdk21ujdkhotspot",
		"Docker.dmg": "docker",
		"Command_Line_Tools_for_Xcode_16_beta_6.dmg": "commandlinetoolsforxcodebeta",
		"kali-linux-2022.1-vmware-amd64.7z":          "kalilinuxvmware",
	}
	for in, want := range cases {
		if got := stem(in); got != want {
			t.Errorf("stem(%q) = %q want %q", in, got, want)
		}
	}
	apps := map[string]string{"docker": "/Applications/Docker.app", "visualstudiocode": "/Applications/Visual Studio Code.app", "xcode": "/Applications/Xcode.app"}
	if got := matchApp("Docker.dmg", apps); got != "/Applications/Docker.app" {
		t.Errorf("Docker.dmg -> %q", got)
	}
	if got := matchApp("VSCode-darwin-universal.zip", apps); got != "" {
		t.Errorf("vscode abbreviation must not match by accident: %q", got)
	}
	if got := matchApp("Visual Studio Code-darwin-arm64.zip", apps); got != "/Applications/Visual Studio Code.app" {
		t.Errorf("VS Code zip -> %q", got)
	}
	if got := matchApp("Xcode_16.dmg", apps); got != "/Applications/Xcode.app" {
		t.Errorf("Xcode -> %q", got)
	}
	if got := matchApp("ab.dmg", apps); got != "" {
		t.Errorf("stems under 3 chars never match: %q", got)
	}
}

func TestScanVerdicts(t *testing.T) {
	home := t.TempDir()
	dl := filepath.Join(home, "Downloads")
	apps := filepath.Join(home, "apps")
	if err := os.MkdirAll(filepath.Join(apps, "Docker.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := AppDirs
	AppDirs = []string{apps}
	t.Cleanup(func() { AppDirs = old })

	w := func(name string, n int) { testutil.Write(t, filepath.Join(dl, name), n) }
	w("Docker.dmg", 3<<20)      // installed
	w("Other-1.2.3.dmg", 2<<20) // installer, not installed
	w("project.zip", 1<<20)     // extracted: project/ beside it
	testutil.Write(t, filepath.Join(dl, "project", "README"), 10)
	w("report.pdf", 4096)
	w("report (1).pdf", 4096)        // copy, same size
	w("report (2).pdf", 8192)        // copy-differs
	w("movie.mp4.crdownload", 1<<20) // partial
	w("empty.bin", 0)                // partial (empty)
	w("old.iso", 5<<20)
	testutil.Age(t, filepath.Join(dl, "old.iso"), 400*24*time.Hour) // stale
	if err := os.MkdirAll(filepath.Join(dl, "Xcode.app", "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, filepath.Join(dl, "Xcode.app", "Contents", "bin"), 1<<20) // app-in-downloads
	if err := os.Symlink(home, filepath.Join(dl, "link")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	res, err := Scan(dl, home, size.Filter{}, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	detail := map[string]string{}
	for _, r := range res.Rows {
		got[r.Name] = r.Verdicts
		detail[r.Name] = r.Detail
	}
	has := func(name, v string) bool {
		for _, x := range got[name] {
			if x == v {
				return true
			}
		}
		return false
	}
	if !has("Docker.dmg", "installed") || !strings.Contains(detail["Docker.dmg"], "Docker.app") {
		t.Errorf("Docker.dmg: %v %q", got["Docker.dmg"], detail["Docker.dmg"])
	}
	if !has("Other-1.2.3.dmg", "installer") || has("Other-1.2.3.dmg", "installed") {
		t.Errorf("Other: %v", got["Other-1.2.3.dmg"])
	}
	if !has("project.zip", "extracted") {
		t.Errorf("project.zip: %v", got["project.zip"])
	}
	if !has("report (1).pdf", "copy") || !has("report (2).pdf", "copy-differs") || len(got["report.pdf"]) != 0 {
		t.Errorf("copies: %v %v %v", got["report (1).pdf"], got["report (2).pdf"], got["report.pdf"])
	}
	if !has("movie.mp4.crdownload", "partial") || !has("empty.bin", "partial") {
		t.Errorf("partials: %v %v", got["movie.mp4.crdownload"], got["empty.bin"])
	}
	if !has("old.iso", "stale") {
		t.Errorf("old.iso: %v", got["old.iso"])
	}
	if !has("Xcode.app", "app-in-downloads") {
		t.Errorf("Xcode.app: %v", got["Xcode.app"])
	}
	if _, ok := got["link"]; ok {
		t.Error("symlinks are skipped")
	}
	if res.ByVerdict["installed"] < 3<<20 || res.Total == 0 {
		t.Errorf("totals: %+v total %d", res.ByVerdict, res.Total)
	}
	if len(res.Rows[0].Verdicts) == 0 {
		t.Errorf("flagged rows first: %+v", res.Rows[0])
	}
	var b strings.Builder
	Print(&b, res, now, false, 0)
	s := b.String()
	// a row line pads the verdict column, so the name follows several spaces; a
	// detail line mentions "as report.pdf" after one space
	if !strings.Contains(s, "flagged") || !strings.Contains(s, "by verdict:") || strings.Contains(s, "   report.pdf\n") || !strings.Contains(s, "nothing was moved") {
		t.Errorf("print:\n%s", s)
	}
	b.Reset()
	Print(&b, res, now, true, 0)
	if !strings.Contains(b.String(), "   report.pdf\n") {
		t.Errorf("verbose shows unflagged rows:\n%s", b.String())
	}
	// extension window narrows the rows
	res, _ = Scan(dl, home, size.Filter{Exts: map[string]bool{"dmg": true}}, now)
	if len(res.Rows) != 2 {
		t.Errorf("ext dmg: %d rows", len(res.Rows))
	}
}
