package protect

import (
	"testing"
)

func withBuiltin(t *testing.T, goos string) {
	t.Helper()
	old := Builtin
	Builtin = BuiltinFor(goos)
	t.Cleanup(func() { Builtin = old })
}

func TestPlatformBuiltins(t *testing.T) {
	home := "/Users/ryan"
	withBuiltin(t, "darwin")
	for _, p := range []string{
		"/Applications/Safari.app",
		"/Library/Keychains/System.keychain",
		"/Library/LaunchDaemons/x.plist",
		"/Library/Application Support/com.apple.TCC/TCC.db",
		"/private/etc/hosts",
		"/private/var/root/.ssh",
		"/private/var/vm/sleepimage",
		"/System/Volumes/Preboot/x",
		home + "/Library/Mobile Documents/com~apple~CloudDocs",
		home + "/Library/CloudStorage/Dropbox",
		home + "/Library/Mail/V10",
		home + "/Library/Messages/chat.db",
		home + "/Library/Application Support/MobileSync/Backup",
		home + "/Library/Cookies/Cookies.binarycookies",
		home + "/Pictures/Photos Library.photoslibrary/database",
		"/Volumes/External/Old.PhotosLibrary/originals",
		home + "/.git-credentials",
		home + "/.config/gh/hosts.yml",
		home + "/.docker/config.json",
	} {
		if _, ok := Hit(p, home, nil); !ok {
			t.Errorf("darwin: %s should be protected", p)
		}
	}
	// What the shipped macOS default cleans must stay reachable.
	for _, p := range []string{
		home + "/Library/Caches/Homebrew",
		home + "/Library/Caches/pip",
		home + "/Library/Developer/Xcode/DerivedData",
		"/Library/Developer/CoreSimulator",
		home + "/Library/Application Support/Code/Cache",
		home + "/Pictures/export.jpg",
	} {
		if hit, ok := Hit(p, home, nil); ok {
			t.Errorf("darwin: %s must stay cleanable, hit %s", p, hit)
		}
	}

	withBuiltin(t, "linux")
	lhome := "/home/ryan"
	for _, p := range []string{
		"/efi/EFI", "/lib32/x", "/libx32/x", "/proc/1", "/sys/kernel", "/dev/sda", "/run/user/1000",
		"/root/.ssh/authorized_keys", "/root/.gnupg/x", "/root/.config/gcloud/x", "/snap/core", "/nix/store/x", "/opt/app",
		"/var/lib/postgresql", "/var/lib/mysql/ibdata1", "/var/lib/dpkg/status", "/var/lib/docker/volumes/db/_data",
		"/var/lib/docker/overlay2/abc", "/var/lib/containerd/io.containerd.content.v1.content",
		lhome + "/.local/share/keyrings/login.keyring", lhome + "/.local/share/kwalletd/x",
		lhome + "/.pki/nssdb", lhome + "/.mozilla/firefox", lhome + "/.netrc", lhome + "/.password-store/x",
		lhome + "/.config/gcloud/credentials.db", lhome + "/.azure/x",
	} {
		if _, ok := Hit(p, lhome, nil); !ok {
			t.Errorf("linux: %s should be protected", p)
		}
	}
	for _, p := range []string{lhome + "/.cache/pip", lhome + "/.cache/uv/archive-v0", lhome + "/.npm/_cacache", "/usr/local/share", "/root/.cache/pip", "/root/.npm/_cacache", "/var/lib/docker", "/var/cache/apt"} {
		if hit, ok := Hit(p, lhome, nil); ok {
			t.Errorf("linux: %s must stay cleanable, hit %s", p, hit)
		}
	}
}

func TestCommandHitsSpellings(t *testing.T) {
	home := "/Users/ryan"
	for _, cmd := range []string{
		"RM -RF /SYSTEM/Library",
		"rm -rf /private/etc/x",
		"rm -rf /System/Volumes/Data/private/var/db/x",
		"rm -rf $HOME/.ssh",
		"rm -rf ${HOME}/.ssh",
		`rm -rf "~/.SSH/id_ed25519"`,
		"PATH=/usr/bin:/etc/foo tool",
		"cp x `echo /etc`",
	} {
		if _, ok := CommandHits(cmd, home, nil); !ok {
			t.Errorf("%q should hit a protected path", cmd)
		}
	}
	// The /private/etc spelling hits the short form on every platform.
	if !mentions(dropAlias("rm /private/etc/x", "/private"), "/etc") {
		t.Error("/private/etc/x must read as /etc/x")
	}
	withBuiltin(t, "darwin")
	if p, ok := CommandHits("rm /private/etc/x", "", nil); !ok || (p != "/private/etc" && p != "/etc") {
		t.Errorf("darwin /private/etc: %q %v", p, ok)
	}
	// never_touch travels in extra.
	if p, ok := CommandHits("tar czf x.tgz ~/Documents/tax", home, []string{home + "/Documents"}); !ok || p != home+"/Documents" {
		t.Errorf("never_touch mention: %q %v", p, ok)
	}
	for _, cmd := range []string{
		"go clean -cache",
		"npm cache clean --force",
		"docker builder prune -f && docker image prune -f",
		"echo /Users/ryan/lib/x /home/me/etc/y",
		"/usr/local/bin/brew cleanup",
		"echo ~user/x",
	} {
		if p, ok := CommandHits(cmd, home, nil); ok {
			t.Errorf("%q must not hit, got %s", cmd, p)
		}
	}
}

func TestInstallerCommandsAcrossPlatforms(t *testing.T) {
	for _, cmd := range []string{
		"/usr/sbin/softwareupdate -i -a",
		"apt-get install -y foo",
		"/usr/bin/apt upgrade",
		"/usr/bin/dpkg --configure -a",
		"/usr/bin/python3 /usr/bin/dnf upgrade -y",
		"/usr/bin/python3.12 /usr/bin/unattended-upgrade",
		"/usr/bin/python2 /usr/bin/yum update",
		"rpm -Uvh x.rpm",
		"pacman -Syu",
		"zypper up",
		"flatpak update",
	} {
		if !InstallerCommand(cmd) {
			t.Errorf("%q is an install", cmd)
		}
	}
	// Resident daemons run on idle systems; matching them would freeze oos.
	for _, cmd := range []string{
		"/usr/libexec/softwareupdated",
		"/usr/lib/snapd/snapd",
		"/usr/libexec/packagekitd",
		"/usr/bin/python3 /usr/share/unattended-upgrades/unattended-upgrade-shutdown --wait-for-signal",
		"/usr/bin/python3 /home/me/app.py",
		"vim /etc/apt/sources.list",
		"grep dpkg log",
	} {
		if InstallerCommand(cmd) {
			t.Errorf("%q must not count as an install", cmd)
		}
	}
}

func TestCommandHitsNormalisedSpellings(t *testing.T) {
	home := "/Users/ryan"
	for cmd, want := range map[string]string{
		`rm -rf "$HOME"/.ssh`:       "~/.ssh",
		`rm -rf ~/'.ssh'`:           "~/.ssh",
		"rm -rf /Users/ryan/./.ssh": "~/.ssh",
		"rm -rf /Users/ryan//.ssh":  "~/.ssh",
		"rm -rf ~ryan/.ssh":         "~/.ssh",
		"rm -rf /usr/local/../bin":  "/usr/bin",
		"rm -rf /":                  "/",
		"rm -rf /*":                 "/",
		"find / -delete":            "/",
		"rm -rf ~":                  "~",
		"rm -rf ~/*":                "~",
		`rm -rf "$HOME"`:            "~",
		"rm -rf /Users/ryan/":       "~",
	} {
		if p, ok := CommandHits(cmd, home, nil); !ok || p != want {
			t.Errorf("%q: got %q %v, want %q", cmd, p, ok, want)
		}
	}
	for _, cmd := range []string{
		"rm -f *.log",
		"find . -name '*.tmp' -delete",
		"rm -rf ~/.cache/foo",
		"rm -rf ~/Library/Caches/*",
		"echo ~other/x",
	} {
		if p, ok := CommandHits(cmd, home, nil); ok {
			t.Errorf("%q must not hit, got %s", cmd, p)
		}
	}
}
