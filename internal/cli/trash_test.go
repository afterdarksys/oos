package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/testutil"
	"github.com/afterdarksys/oos/internal/trash"
)

func TestEmptyTrashIsPermanentAndDryRun(t *testing.T) {
	home := t.TempDir()
	old := trash.VolumeRoot
	trash.VolumeRoot = t.TempDir()
	t.Cleanup(func() { trash.VolumeRoot = old })
	// macOS keeps ~/.Trash; Linux the freedesktop bin, where a trashed
	// file has a payload under files/ and a record under info/.
	bin := filepath.Join(home, ".Trash")
	trashed := []string{filepath.Join(bin, "a.txt")}
	keep := []string{bin}
	if runtime.GOOS != "darwin" {
		bin = filepath.Join(home, ".local", "share", "Trash")
		trashed = []string{filepath.Join(bin, "files", "a.txt"), filepath.Join(bin, "info", "a.txt.trashinfo")}
		keep = []string{bin, filepath.Join(bin, "files"), filepath.Join(bin, "info")}
	}
	for _, f := range trashed {
		testutil.Write(t, f, 100)
	}
	p := testutil.PolicyFor(home)
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	env := guard.Env{Home: home}
	var out, errw bytes.Buffer
	if code := doTrash(cfg, env, &opts{emptyTrash: true}, time.Now(), &out, &errw); code != status.ExitOK {
		t.Fatalf("dry-run code %d %s", code, errw.String())
	}
	for _, f := range trashed {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("dry-run removed %s", f)
		}
	}
	if !strings.Contains(out.String(), "permanent") || !strings.Contains(out.String(), "not quarantine") {
		t.Fatalf("dry-run text:\n%s", out.String())
	}
	out.Reset()
	if code := doTrash(cfg, env, &opts{emptyTrash: true, yes: true}, time.Now(), &out, &errw); code != status.ExitOK {
		t.Fatalf("live code %d\n%s\n%s", code, out.String(), errw.String())
	}
	for _, f := range trashed {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("live empty left %s", f)
		}
	}
	for _, d := range keep {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("the trash directory %s must stay", d)
		}
	}
	logb, err := os.ReadFile(p.LogFile)
	if err != nil || !strings.Contains(string(logb), "empty-trash") {
		t.Fatalf("audit log: %v %s", err, logb)
	}
}
