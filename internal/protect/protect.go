// Package protect is the always_disallowed list. It is hard-coded. A config
// file can add paths to it and cannot remove any. never_touch and
// allow_outside_home do not apply. /usr/local is the local hierarchy, not
// the OS, and is left alone unless a config adds it explicitly.
package protect

import (
	"os"
	"path/filepath"
	"strings"
)

// Builtin is the always_disallowed list compiled into the binary. "/" matches
// only itself. Entries starting with "~/" are resolved against the home
// directory at check time. /usr is the operating system; /usr/local is not.
var Builtin = []string{
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
}

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
		if match(path, raw, home) {
			return raw, true
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
// the config. /usr/local is not a hit unless extra lists it.
func CommandHits(cmd, home string, extra []string) (string, bool) {
	if strings.TrimSpace(cmd) == "" {
		return "", false
	}
	if prefix, ok := scanCommand(cmd, home, extra); ok {
		return prefix, true
	}
	return scanCommand(cmd, home, Builtin)
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
			if mentions(cmd, p) {
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
	case ' ', '\t', '\n', '"', '\'', '=', ';', '|', '&', '<', '>', '(', ')':
		return true
	default:
		return false
	}
}

// installBins are process basenames that mean an OS or package install is
// writing the machine. softwareupdated is the always-on daemon and is not
// in this list.
var installBins = []string{
	"osinstallersetupd",
	"InstallAssistant",
	"startosinstall",
	"installer",
}

// InstallerCommand reports whether one process command line is an installer.
func InstallerCommand(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	base := filepath.Base(fields[0])
	for _, n := range installBins {
		if base == n {
			return true
		}
	}
	return strings.Contains(cmd, "Install macOS")
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
