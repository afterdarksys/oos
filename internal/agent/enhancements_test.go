package agent

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/afterdarksys/oos/internal/config"
)

func TestAgentFilesAndInstall(t *testing.T) {
	home := t.TempDir()
	files := Files(home, "/usr/local/bin/oos")
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		if len(files) != 0 {
			t.Error("unsupported platform should return no files")
		}
		return
	}
	if len(files) == 0 {
		t.Fatal("expected agent files")
	}
	var ran [][]string
	run := func(name string, args ...string) error {
		ran = append(ran, append([]string{name}, args...))
		return nil
	}
	if err := Install(home, "/usr/local/bin/oos", false, run); err != nil {
		t.Fatal(err)
	}
	for p, want := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("agent file not written: %s", p)
		}
		if string(b) != want || !strings.Contains(string(b), "/usr/local/bin/oos") {
			t.Errorf("agent file content mismatch for %s", p)
		}
		if !config.IsUnder(p, home) {
			t.Errorf("agent file %s must live under home", p)
		}
	}
	if len(ran) == 0 {
		t.Error("install should invoke the service manager")
	}
	if err := Uninstall(home, false, run); err != nil {
		t.Fatal(err)
	}
	for p := range files {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("agent file should be removed: %s", p)
		}
	}
}
