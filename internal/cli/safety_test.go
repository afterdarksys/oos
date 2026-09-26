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

func TestEmptyTrashRefusedDuringInstall(t *testing.T) {
	home := t.TempDir()
	old := trash.VolumeRoot
	trash.VolumeRoot = t.TempDir()
	t.Cleanup(func() { trash.VolumeRoot = old })
	item := filepath.Join(home, ".Trash", "a.txt")
	testutil.Write(t, item, 100)
	cfg := &config.Config{Version: 1, Volume: home, Policy: testutil.PolicyFor(home), Home: home}
	for name, procs := range map[string]func() ([]string, error){
		"installer running":  func() ([]string, error) { return []string{"/usr/sbin/softwareupdate -i -a"}, nil },
		"process list fails": func() ([]string, error) { return nil, os.ErrPermission },
	} {
		var out, errw bytes.Buffer
		env := guard.Env{Home: home, Procs: procs}
		if code := doTrash(cfg, env, &opts{emptyTrash: true, yes: true}, time.Now(), &out, &errw); code != status.ExitCritical {
			t.Errorf("%s: code %d, want critical", name, code)
		}
		if !strings.Contains(errw.String(), "install") {
			t.Errorf("%s: message %q", name, errw.String())
		}
		if _, err := os.Stat(item); err != nil {
			t.Fatalf("%s: trash emptied during an install", name)
		}
	}
}

func TestAddRefusesConfigOthersCanWrite(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "oos.json")
	if err := os.WriteFile(cfgPath, config.DefaultFor("darwin"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfgPath, 0o666); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(cfgPath)
	target := filepath.Join(home, "proj", "target")
	testutil.Write(t, filepath.Join(target, "x"), 1)
	var out, errw bytes.Buffer
	o := &opts{config: cfgPath, add: target, addType: "build", addAction: config.ActionRmContents}
	if code := doAdd(guard.Env{Home: home}, o, &out, &errw); code == status.ExitOK {
		t.Fatal("--add must refuse a world-writable config")
	}
	if !strings.Contains(errw.String(), "writable by group or others") {
		t.Fatalf("message %q", errw.String())
	}
	after, _ := os.ReadFile(cfgPath)
	if !bytes.Equal(before, after) {
		t.Fatal("the config was rewritten")
	}
}

func TestAddIgnoresWorkingDirectoryConfig(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	local := filepath.Join(cwd, "oos.json")
	if err := os.WriteFile(local, config.DefaultFor("darwin"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	got, err := configFileForEdit(&opts{}, home)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(home, ".config", "oos", "oos.json") {
		t.Fatalf("--add must edit ~/.config/oos/oos.json, got %s", got)
	}
}
