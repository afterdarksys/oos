// Package protect is the always_disallowed list. It is hard-coded. A config
// file can add paths to it and cannot remove any. never_touch and
// allow_outside_home do not apply. /usr/local is the local hierarchy, not
// the OS, and is left alone unless a config adds it explicitly.
package protect

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Builtin is the always_disallowed list compiled into the binary for this
// platform. "/" matches only itself. Entries starting with "~/" are resolved
// against the home directory at check time. /usr is the operating system;
// /usr/local is not.
var Builtin = BuiltinFor(runtime.GOOS)

// common applies on every platform. Credential stores are listed here even
// when they began life on one OS: a token file is a token file.
var common = []string{
	"/",
	"/System",
	"/usr",
	"/bin",
	"/sbin",
	"/private/var/db",
	"/var/db",
	"/Library/Updates",
	"/Library/Apple",
	"/boot",
	"/etc",
	"/lib",
	"/lib64",
	"/var/lib/dpkg",
	"/var/lib/rpm",
	"/var/lib/apt",
	"/macOS Install Data",
	"~/.ssh",
	"~/.gnupg",
	"~/.aws",
	"~/.kube",
	"~/Library/Keychains",
	"~/.password-store",
	"~/.netrc",
	"~/.git-credentials",
	"~/.docker",
	"~/.config/gcloud",
	"~/.azure",
	"~/.config/gh",
}

// darwin protects system configuration and user data stores. /Library as a
// whole is not listed: /Library/Developer/CoreSimulator is a shipped default
// entry, so only the subdirectories that hold system state are.
var darwin = []string{
	"/Applications",
	"/Library/Keychains",
	"/Library/LaunchDaemons",
	"/Library/LaunchAgents",
	"/Library/Preferences",
	"/Library/Extensions",
	"/Library/Application Support/com.apple.TCC",
	"/private/etc",
	"/private/var/root",
	"/var/root",
	"/private/var/vm",
	"/var/vm",
	"/private/var/protected",
	"/var/protected",
	"/System/Volumes/Preboot",
	"/System/Volumes/Recovery",
	"~/Library/Mobile Documents",
	"~/Library/CloudStorage",
	"~/Library/Mail",
	"~/Library/Messages",
	"~/Library/Application Support/MobileSync",
	"~/Library/Accounts",
	"~/Library/Cookies",
	"~/Pictures/Photos Library.photoslibrary",
}

// linux protects the FHS system trees and per-user secret stores.
//
// /var/lib and /root are protected whole: every service keeps state there
// (Proxmox /var/lib/vz, tailscale, vault, consul, ceph, /root/backups) and an
// enumerated list fails open on the next unlisted one. The few cache anchors
// that are safe to clean are carved out in exceptions, the way /usr/local is
// carved out of /usr. /var/lib/docker is excepted as that exact path only, the
// anchor for "docker builder prune"; everything below it stays protected, and
// its image, layer, container and volume stores are also listed by name so
// that removal, which refuses an ancestor of a protected path, keeps refusing
// rm-contents of /var/lib/docker itself. The /var/lib and /root children
// below are covered by the whole-tree entries and stay listed as a record.
var linux = []string{
	"/efi",
	"/lib32",
	"/libx32",
	"/proc",
	"/sys",
	"/dev",
	"/run",
	"/snap",
	"/nix",
	"/opt",
	"/var/lib",
	"/root",
	"/var/lib/pacman",
	"/var/lib/dnf",
	"/var/lib/yum",
	"/var/lib/ucf",
	"/var/lib/containerd",
	"/var/lib/containers",
	"/var/lib/docker/volumes",
	"/var/lib/docker/image",
	"/var/lib/docker/overlay2",
	"/var/lib/docker/containers",
	"/var/lib/docker/swarm",
	"/var/lib/docker/network",
	"/var/lib/docker/plugins",
	"/var/lib/docker/buildkit",
	"/var/lib/mysql",
	"/var/lib/mariadb",
	"/var/lib/postgresql",
	"/var/lib/pgsql",
	"/var/lib/mongodb",
	"/var/lib/redis",
	"/var/lib/etcd",
	"/var/lib/elasticsearch",
	"/var/lib/rabbitmq",
	"/var/lib/cassandra",
	"/var/lib/influxdb",
	"/var/lib/kubelet",
	"/var/lib/rancher",
	"/var/lib/snapd",
	"/var/lib/flatpak",
	"/var/lib/systemd",
	"/var/lib/private",
	"/var/lib/NetworkManager",
	"/var/lib/sss",
	"/var/lib/polkit-1",
	"/var/lib/AccountsService",
	"/var/lib/libvirt",
	"/var/lib/lxc",
	"/var/lib/lxd",
	"/var/lib/incus",
	"/var/lib/machines",
	"/root/.ssh",
	"/root/.gnupg",
	"/root/.aws",
	"/root/.kube",
	"/root/.docker",
	"/root/.config/gcloud",
	"/root/.azure",
	"/root/.config/gh",
	"/root/.netrc",
	"/root/.git-credentials",
	"/root/.password-store",
	"/root/.pki",
	"/root/.local/share/keyrings",
	"~/.local/share/keyrings",
	"~/.local/share/kwalletd",
	"~/.pki",
	"~/.mozilla",
}

// BuiltinFor is the built-in list for goos.
func BuiltinFor(goos string) []string {
	out := append([]string{}, common...)
	switch goos {
	case "darwin":
		out = append(out, darwin...)
	case "linux":
		out = append(out, linux...)
	}
	return out
}

// exception carves path out of the built-in entry base. subtree allows
// everything below path too; otherwise only path itself.
type exception struct {
	base, path string
	subtree    bool
}

// exceptions are the cache anchors below a whole-tree entry that oos may
// clean. They apply on every platform but only matter where base is listed
// (Linux). Matching is on path boundaries: /root/.cache-evil is not
// /root/.cache. A config's always_disallowed still wins over them.
var exceptions = []exception{
	{"/var/lib", "/var/lib/docker", false},
	{"/root", "/root/.cache", true},
	{"/root", "/root/.npm", true},
	{"/root", "/root/.cargo/registry", true},
	{"/root", "/root/go/pkg/mod", true},
	{"/root", "/root/.gradle/caches", true},
	{"/root", "/root/.m2/repository", true},
	// oos's own state, log and quarantine store when root runs it with
	// HOME=/root; removal refuses them separately as own_data.
	{"/root", "/root/.local/state/oos", true},
}

// Excepted reports whether path is carved out of the built-in entry base,
// so that entry alone does not protect it. Removal checks that walk the
// built-in list themselves call it for each entry.
func Excepted(path, base string) bool {
	path, base = filepath.Clean(path), filepath.Clean(base)
	for _, e := range exceptions {
		if e.base == base && (path == e.path || (e.subtree && under(path, e.path))) {
			return true
		}
	}
	return false
}

// PhotosLibrary is the bundle suffix Photos uses. A library can live
// anywhere, so any path component carrying it is protected, not only the
// default ~/Pictures location. Detecting a library below a directory that
// is about to be removed would need a walk; the removal walk does not look
// for bundles, so the ancestor case is covered only by never_touch.
const PhotosLibrary = ".photoslibrary"

// InstallDataDirs are created while a macOS upgrade is staged. Tests point
// them at a temporary directory. A non-empty directory means an install is
// in progress; an empty leftover does not freeze the tool.
var InstallDataDirs = []string{
	"/macOS Install Data",
	"/System/Volumes/Data/macOS Install Data",
}

// OSPath reports the built-in absolute prefix path is under. Home-relative
// entries are not considered; Hit is the full check.
func OSPath(path string) (string, bool) {
	return Hit(path, "", nil)
}

// Hit reports the always_disallowed entry that covers path. extra are paths
// from policy.always_disallowed, already expanded. They are added to Builtin.
// The path is not resolved through symlinks or firmlinks, so /Users stays
// usable when the data volume also spells it under /System/Volumes/Data.
func Hit(path, home string, extra []string) (string, bool) {
	path = filepath.Clean(path)
	for _, raw := range extra {
		if match(path, raw, home) {
			return raw, true
		}
	}
	if path == "/usr/local" || under(path, "/usr/local") {
		return "", false
	}
	for _, raw := range Builtin {
		if match(path, raw, home) && !Excepted(path, raw) {
			return raw, true
		}
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if len(part) > len(PhotosLibrary) && strings.HasSuffix(strings.ToLower(part), PhotosLibrary) {
			return "*" + PhotosLibrary, true
		}
	}
	return "", false
}

func match(path, raw, home string) bool {
	if raw == "/" {
		return path == "/"
	}
	base := raw
	if strings.HasPrefix(raw, "~/") || raw == "~" {
		if home == "" {
			return false
		}
		base = filepath.Clean(home + strings.TrimPrefix(raw, "~"))
	} else {
		base = filepath.Clean(raw)
	}
	return path == base || under(path, base)
}

func under(path, base string) bool {
	return strings.HasPrefix(path, base+string(filepath.Separator))
}

// CommandHitsOS reports when a shell command names a built-in absolute path.
func CommandHitsOS(cmd string) (string, bool) {
	return CommandHits(cmd, "", nil)
}

// CommandHits reports when a shell command names an always_disallowed path,
// including home-relative entries once home is known, and any extra from
// the config (callers pass never_touch here too). /usr/local is not a hit
// unless extra lists it.
//
// The scan is advisory. It reads the command text, not what the shell will
// do: a variable, a glob, a cd, or a script the command runs can reach a
// protected path without naming it. It catches the plain mistakes; the real
// gate for commands is allow_commands, which defaults to false. Matching is
// case-insensitive (APFS and HFS+ are), $HOME, ${HOME} and ~ are expanded,
// and the /private and /System/Volumes/Data spellings of a path are also
// read as the short form, so /private/etc/x names /etc.
func CommandHits(cmd, home string, extra []string) (string, bool) {
	if strings.TrimSpace(cmd) == "" {
		return "", false
	}
	for _, form := range commandForms(cmd, home) {
		if prefix, ok := wholeTree(form, home); ok {
			return prefix, true
		}
		if prefix, ok := scanCommand(form, home, extra); ok {
			return prefix, true
		}
		if prefix, ok := scanCommand(form, home, Builtin); ok {
			return prefix, true
		}
	}
	return "", false
}

// commandForms is the lowercased command with quotes dropped, the home
// spellings expanded and each absolute path cleaned (so "$HOME"/.ssh,
// /x//y and /usr/local/../bin read as the paths they name), followed by the
// same text with the firmlink and /private aliases removed.
func commandForms(cmd, home string) []string {
	cmd = strings.ToLower(cmd)
	cmd = strings.NewReplacer(`"`, "", `'`, "").Replace(cmd)
	if home != "" {
		h := strings.ToLower(filepath.Clean(home))
		cmd = strings.ReplaceAll(cmd, "${home}", h)
		cmd = strings.ReplaceAll(cmd, "$home", h)
		cmd = expandTilde(cmd, "~"+filepath.Base(h), h)
		cmd = expandTilde(cmd, "~", h)
	}
	cmd = cleanPaths(cmd)
	forms := []string{cmd}
	for _, alias := range []string{"/system/volumes/data", "/private"} {
		if short := dropAlias(cmd, alias); short != cmd {
			forms = append(forms, short)
		}
	}
	return forms
}

// expandTilde replaces tilde (~, or ~user for the home's own user) where it
// starts a word and is followed by "/" or a word boundary. Other ~user forms
// and a ~ inside a word are left alone.
func expandTilde(cmd, tilde, home string) string {
	var b strings.Builder
	for i := 0; i < len(cmd); {
		end := i + len(tilde)
		if strings.HasPrefix(cmd[i:], tilde) && (i == 0 || isBoundary(cmd[i-1])) && (end == len(cmd) || cmd[end] == '/' || isBoundary(cmd[end])) {
			b.WriteString(home)
			i = end
			continue
		}
		b.WriteByte(cmd[i])
		i++
	}
	return b.String()
}

// cleanPaths runs filepath.Clean over every word that starts with "/".
func cleanPaths(cmd string) string {
	var b strings.Builder
	for i := 0; i < len(cmd); {
		if cmd[i] != '/' || (i > 0 && !isBoundary(cmd[i-1])) {
			b.WriteByte(cmd[i])
			i++
			continue
		}
		j := i
		for j < len(cmd) && !isBoundary(cmd[j]) {
			j++
		}
		b.WriteString(filepath.Clean(cmd[i:j]))
		i = j
	}
	return b.String()
}

// wholeTree reports a word that names the root or the home itself, alone or
// with a trailing glob: rm -rf /, find / -delete, rm -rf ~/*.
func wholeTree(cmd, home string) (string, bool) {
	h := ""
	if home != "" {
		h = strings.ToLower(filepath.Clean(home))
	}
	for _, w := range strings.FieldsFunc(cmd, func(r rune) bool { return r < 128 && isBoundary(byte(r)) }) {
		w = strings.TrimRight(w, "*")
		if w == "/" {
			return "/", true
		}
		if h != "" && strings.TrimSuffix(w, "/") == h {
			return "~", true
		}
	}
	return "", false
}

// dropAlias removes alias where it starts a word and is followed by "/".
func dropAlias(cmd, alias string) string {
	var b strings.Builder
	for i := 0; i < len(cmd); {
		if strings.HasPrefix(cmd[i:], alias+"/") && (i == 0 || isBoundary(cmd[i-1])) {
			i += len(alias)
			continue
		}
		b.WriteByte(cmd[i])
		i++
	}
	return b.String()
}

func scanCommand(cmd, home string, list []string) (string, bool) {
	ordered := append([]string{"/usr/bin", "/usr/sbin", "/usr/libexec", "/usr/lib", "/usr/share"}, list...)
	for _, raw := range ordered {
		if raw == "/" {
			continue
		}
		cands := []string{raw}
		if strings.HasPrefix(raw, "~/") && home != "" {
			cands = append(cands, filepath.Clean(home+strings.TrimPrefix(raw, "~")))
		}
		for _, p := range cands {
			if mentions(cmd, strings.ToLower(p)) {
				return raw, true
			}
		}
	}
	return "", false
}

func mentions(cmd, prefix string) bool {
	rest := cmd
	for {
		i := strings.Index(rest, prefix)
		if i < 0 {
			return false
		}
		at := len(cmd) - len(rest) + i
		before := at == 0 || isBoundary(cmd[at-1])
		end := at + len(prefix)
		after := end >= len(cmd) || cmd[end] == '/' || isBoundary(cmd[end])
		if before && after && !localUsr(cmd, at, prefix) {
			return true
		}
		rest = rest[i+len(prefix):]
	}
}

func localUsr(cmd string, at int, prefix string) bool {
	if prefix != "/usr" && !strings.HasPrefix(prefix, "/usr/") {
		return false
	}
	end := at + len("/usr/local")
	return strings.HasPrefix(cmd[at:], "/usr/local") && (end >= len(cmd) || cmd[end] == '/' || isBoundary(cmd[end]))
}

func isBoundary(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '"', '\'', '=', ';', '|', '&', '<', '>', '(', ')', ':', ',', '`', '{', '}':
		return true
	default:
		return false
	}
}

// installBins are process basenames that mean an OS or package install is
// writing the machine. Only programs that run for the length of an install
// are listed; anything resident on an idle system would freeze oos forever.
// softwareupdated (macOS), snapd and packagekitd (Linux) are daemons that
// stay up between installs and are not in this list. Package manager lock
// files are not used either: dpkg and rpm locks exist while idle.
var installBins = []string{
	"osinstallersetupd",
	"InstallAssistant",
	"startosinstall",
	"installer",
	"softwareupdate",
	"apt",
	"apt-get",
	"dpkg",
	"dnf",
	"yum",
	"rpm",
	"pacman",
	"zypper",
	"unattended-upgr",
	"unattended-upgrade",
	"flatpak",
}

// interpreters run package managers written as scripts (dnf, yum,
// unattended-upgrade); the program is then the first argument.
var interpreters = []string{"python", "python2", "python3", "perl", "sh", "bash", "dash"}

// InstallerCommand reports whether one process command line is an installer.
func InstallerCommand(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	base := filepath.Base(fields[0])
	if len(fields) > 1 && isInterpreter(base) {
		base = filepath.Base(fields[1])
	}
	for _, n := range installBins {
		if base == n {
			return true
		}
	}
	return strings.Contains(cmd, "Install macOS")
}

func isInterpreter(base string) bool {
	for _, n := range interpreters {
		if base == n || strings.HasPrefix(base, n+".") {
			return true
		}
	}
	return false
}

// StagedInstall reports a macOS upgrade payload still on disk.
func StagedInstall() (string, bool) {
	for _, d := range InstallDataDirs {
		fi, err := os.Lstat(d)
		if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		ents, err := os.ReadDir(d)
		if err != nil || len(ents) == 0 {
			continue
		}
		return d, true
	}
	return "", false
}

// InstallerRunning reports the first installer process in cmds.
func InstallerRunning(cmds []string) (string, bool) {
	for _, c := range cmds {
		if InstallerCommand(c) {
			return c, true
		}
	}
	return "", false
}
