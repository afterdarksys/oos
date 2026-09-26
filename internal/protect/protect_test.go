package protect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOSPath(t *testing.T) {
	hits := []string{
		"/",
		"/System",
		"/System/Library/CoreServices",
		"/System/Volumes/Data/Users/ryan",
		"/usr/bin/true",
		"/bin",
		"/private/var/db/softwareupdate",
		"/Library/Updates/index.plist",
		"/macOS Install Data/payload",
		"/etc/passwd",
		"/boot/vmlinuz",
		"/var/lib/dpkg/status",
	}
	for _, p := range hits {
		if _, ok := OSPath(p); !ok {
			t.Errorf("%s should be refused", p)
		}
	}
	ok := []string{
		"/Users/ryan/Library/Caches",
		"/usr/local",
		"/usr/local/var/cache",
		"/Library/Developer/CoreSimulator",
		"/Users/ryan/bin/tool",
		"/home/ryan/.cache",
	}
	for _, p := range ok {
		if hit, no := OSPath(p); no {
			t.Errorf("%s should be allowed, hit %s", p, hit)
		}
	}
}

func TestCommandHitsOS(t *testing.T) {
	if p, ok := CommandHitsOS("rm -rf /System/Library"); !ok || p != "/System" {
		t.Fatalf("system: %q %v", p, ok)
	}
	if _, ok := CommandHitsOS("rm -rf /usr/bin/true"); !ok {
		t.Fatal("/usr/bin must be refused")
	}
	if _, ok := CommandHitsOS("/usr/local/bin/foo clean"); ok {
		t.Fatal("/usr/local is the local hierarchy")
	}
	if _, ok := CommandHitsOS("xcrun simctl delete unavailable"); ok {
		t.Fatal("a command that does not name an OS path")
	}
	if _, ok := CommandHitsOS("echo /Users/ryan/bin"); ok {
		t.Fatal("a home bin is not /bin")
	}
}

func TestStagedInstallAndProcesses(t *testing.T) {
	dir := t.TempDir()
	old := InstallDataDirs
	InstallDataDirs = []string{filepath.Join(dir, "macOS Install Data")}
	t.Cleanup(func() { InstallDataDirs = old })
	if _, ok := StagedInstall(); ok {
		t.Fatal("missing payload must not freeze the tool")
	}
	if err := os.MkdirAll(InstallDataDirs[0], 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := StagedInstall(); ok {
		t.Fatal("an empty leftover directory is not an install")
	}
	if err := os.WriteFile(filepath.Join(InstallDataDirs[0], "payload"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if why, ok := StagedInstall(); !ok || why == "" {
		t.Fatal("a payload must be reported")
	}
}

func TestAlwaysDisallowedAddsAndCannotDrop(t *testing.T) {
	home := "/Users/ryan"
	if got, ok := Hit(home+"/.ssh/id_ed25519", home, nil); !ok || got != "~/.ssh" {
		t.Fatalf("ssh key: %q %v", got, ok)
	}
	if _, ok := Hit(home+"/.ssh/id_ed25519", "", nil); ok {
		t.Fatal("home-relative entries need a home directory")
	}
	extra := home + "/secrets"
	if got, ok := Hit(extra+"/k", home, []string{extra}); !ok || got != extra {
		t.Fatalf("config addition: %q %v", got, ok)
	}
	// Dropping /System from the config list must not lift it: Hit with a
	// nil extra still refuses it.
	if _, ok := Hit("/System/Library", home, nil); !ok {
		t.Fatal("built-in /System is always disallowed")
	}
	if p, ok := CommandHits("rm -rf ~/.ssh", home, nil); !ok || p != "~/.ssh" {
		t.Fatalf("command ~/.ssh: %q %v", p, ok)
	}
	if p, ok := CommandHits("shred "+home+"/.gnupg/key", home, nil); !ok || p != "~/.gnupg" {
		t.Fatalf("command gnupg: %q %v", p, ok)
	}
}

func TestStagedInstallSoftwareupdated(t *testing.T) {
	if _, ok := InstallerRunning([]string{"/usr/libexec/softwareupdated"}); ok {
		t.Fatal("softwareupdated is always running and is not an install")
	}
	if cmd, ok := InstallerRunning([]string{"/System/Library/CoreServices/osinstallersetupd"}); !ok || cmd == "" {
		t.Fatal("osinstallersetupd is an install")
	}
}
