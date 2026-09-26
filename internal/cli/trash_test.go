package cli

import (
	"bytes"
	"os"
	"path/filepath"
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
	testutil.Write(t, filepath.Join(home, ".Trash", "a.txt"), 100)
	p := testutil.PolicyFor(home)
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	env := guard.Env{Home: home}
	var out, errw bytes.Buffer
	if code := doTrash(cfg, env, &opts{emptyTrash: true}, time.Now(), &out, &errw); code != status.ExitOK {
		t.Fatalf("dry-run code %d %s", code, errw.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".Trash", "a.txt")); err != nil {
		t.Fatal("dry-run removed the file")
	}
	if !strings.Contains(out.String(), "permanent") || !strings.Contains(out.String(), "not quarantine") {
		t.Fatalf("dry-run text:\n%s", out.String())
	}
	out.Reset()
	if code := doTrash(cfg, env, &opts{emptyTrash: true, yes: true}, time.Now(), &out, &errw); code != status.ExitOK {
		t.Fatalf("live code %d\n%s\n%s", code, out.String(), errw.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".Trash", "a.txt")); !os.IsNotExist(err) {
		t.Fatal("live empty left the file")
	}
	if _, err := os.Stat(filepath.Join(home, ".Trash")); err != nil {
		t.Fatal("the trash directory must stay")
	}
	logb, err := os.ReadFile(p.LogFile)
	if err != nil || !strings.Contains(string(logb), "empty-trash") {
		t.Fatalf("audit log: %v %s", err, logb)
	}
}
