package protect_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/protect"
)

func linuxBuiltin(t *testing.T) {
	t.Helper()
	old := protect.Builtin
	protect.Builtin = protect.BuiltinFor("linux")
	t.Cleanup(func() { protect.Builtin = old })
}

// The shipped fleet config must load as root on Linux. A built-in that
// protects /root and /var/lib whole must except every anchor it uses.
func TestServerConfigLoadsWithLinuxBuiltins(t *testing.T) {
	linuxBuiltin(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "oos.server.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(b, "/root")
	if err != nil {
		t.Fatalf("deploy/oos.server.json must load with linux builtins: %v", err)
	}
	if len(cfg.KnownDirs) == 0 {
		t.Fatal("server config lists no known dirs")
	}
}

// Docker, containerd and database state stay refused as destructive
// entries; only the listed cache anchors are carved out of /var/lib.
func TestLinuxServiceStateRefused(t *testing.T) {
	linuxBuiltin(t)
	entry := func(path, action string) string {
		return `{"version":1,"volume":"/","policy":{"min_free_gb":1,"warn_free_gb":2,"max_delete_gb_per_run":1,"min_path_depth":3,"allow_outside_home":true,"log_file":"/root/l","state_file":"/root/s","big_file_min_mb":1,"scan_top_n":1},"known_dirs":[{"path":"` + path + `","type":"x","action":"` + action + `"}]}`
	}
	for _, p := range []string{
		"/var/lib/docker/volumes",
		"/var/lib/docker/overlay2",
		"/var/lib/mysql",
		"/var/lib/postgresql/16/main",
		"/var/lib/containerd",
		"/var/lib/kubelet/pods",
		"/root/.ssh",
	} {
		_, err := config.Parse([]byte(entry(p, "rm-contents")), "/root")
		if err == nil || !strings.Contains(err.Error(), "always_disallowed") {
			t.Errorf("rm-contents %s must be refused, got %v", p, err)
		}
	}
	if _, err := config.Parse([]byte(entry("/root/.cache/pip", "rm-contents")), "/root"); err != nil {
		t.Errorf("root's pip cache must stay cleanable: %v", err)
	}
	// Emptying /var/lib/docker would take the protected children with it.
	// The run-time check opens the parent first, so a path whose parent is
	// missing on this machine is skipped.
	p := config.Policy{AllowOutsideHome: true}
	for _, dir := range []string{"/var/lib/docker", "/var/lib/docker/volumes", "/var/lib/mysql"} {
		if fi, err := os.Stat(filepath.Dir(dir)); err != nil || !fi.IsDir() {
			continue
		}
		err := guard.CheckRemovalPath(p, dir, "/root")
		var r *guard.Refusal
		if !errors.As(err, &r) || r.Rule != "always_disallowed" {
			t.Errorf("removal of %s must be refused as always_disallowed, got %v", dir, err)
		}
	}
}

func serverEntry(path, action, cmd string) string {
	c := ""
	if cmd != "" {
		c = `,"command":"` + cmd + `"`
	}
	return `{"version":1,"volume":"/","policy":{"min_free_gb":1,"warn_free_gb":2,"max_delete_gb_per_run":1,"min_path_depth":3,"allow_outside_home":true,"allow_commands":true,"log_file":"/root/.local/state/oos/l","state_file":"/root/.local/state/oos/s","big_file_min_mb":1,"scan_top_n":1},"known_dirs":[{"path":"` + path + `","type":"x","action":"` + action + `"` + c + `}]}`
}

// /var/lib and /root are protected whole. Unlisted service state and root's
// data stay refused; only exact cache anchors (on path boundaries) are not.
func TestLinuxWholeTreesWithExceptions(t *testing.T) {
	linuxBuiltin(t)
	for _, p := range []string{
		"/var/lib/vz",
		"/var/lib/pve-cluster",
		"/var/lib/tailscale",
		"/var/lib/prometheus",
		"/var/lib/grafana",
		"/var/lib/vault",
		"/var/lib/consul",
		"/var/lib/ceph",
		"/var/lib/docker/tmp",
		"/var/lib/docker-evil",
		"/var/lib",
		"/root",
		"/root/backups",
		"/root/data",
		"/root/.cache-evil",
		"/root/.npmrc-dir",
		"/root/go/pkg",
		"/root/.local/state/oosx",
		"~/.ssh",
		"~/.gnupg/private-keys-v1.d",
	} {
		_, err := config.Parse([]byte(serverEntry(p, "rm-contents", "")), "/root")
		if err == nil || !strings.Contains(err.Error(), "always_disallowed") {
			t.Errorf("rm-contents %s must be refused, got %v", p, err)
		}
	}
	for _, p := range []string{
		"/root/.cache/pip",
		"/root/.cache",
		"~/.cache/pip",
		"/root/.npm/_cacache",
		"/root/.cargo/registry",
		"/root/go/pkg/mod",
		"/root/.gradle/caches",
		"/root/.m2/repository",
	} {
		if _, err := config.Parse([]byte(serverEntry(p, "rm-contents", "")), "/root"); err != nil {
			t.Errorf("rm-contents %s must stay allowed: %v", p, err)
		}
	}
	if _, err := config.Parse([]byte(serverEntry("/var/lib/docker", "command", "docker builder prune -f")), "/root"); err != nil {
		t.Errorf("/var/lib/docker command anchor must load: %v", err)
	}
	for _, c := range []struct{ path, base string }{
		{"/var/lib/docker", "/var/lib"},
		{"/root/.cache", "/root"},
		{"/root/.cache/pip/wheels", "/root"},
	} {
		if !protect.Excepted(c.path, c.base) {
			t.Errorf("%s should be excepted from %s", c.path, c.base)
		}
	}
	for _, c := range []struct{ path, base string }{
		{"/var/lib/docker/volumes", "/var/lib"},
		{"/var/lib/dockerx", "/var/lib"},
		{"/root/.cache-evil", "/root"},
		{"/root/.cache", "/var/lib"},
		{"/root", "/root"},
	} {
		if protect.Excepted(c.path, c.base) {
			t.Errorf("%s must not be excepted from %s", c.path, c.base)
		}
	}
}

// The embedded Linux default still loads when root runs it with HOME=/root:
// its cache entries and quarantine store are excepted anchors.
func TestLinuxDefaultLoadsAsRoot(t *testing.T) {
	linuxBuiltin(t)
	if _, err := config.Parse(config.DefaultFor("linux"), "/root"); err != nil {
		t.Fatalf("linux default must load with HOME=/root: %v", err)
	}
}
