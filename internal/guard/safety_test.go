package guard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/protect"
	"github.com/afterdarksys/oos/internal/testutil"
	"github.com/afterdarksys/oos/internal/worklimit"
)

func TestCommandEntriesObeyPathRules(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	p.QuarantineDir = filepath.Join(home, ".local", "state", "oos", "quarantine")
	testutil.Write(t, filepath.Join(p.QuarantineDir, "b1", "x"), 1)
	env := Env{Home: home, Procs: testutil.NoProcs}
	keep := filepath.Join(home, "keep", "cache")
	cache := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(keep, "f"), 1)
	testutil.Write(t, filepath.Join(cache, "f"), 1)
	cases := []struct {
		name, path, cmd, rule string
	}{
		{"never_touch path", keep, "go clean -cache", "never_touch"},
		{"outside home", t.TempDir(), "go clean -cache", "home"},
		// ~/.local/state holds the quarantine but no built-in protected path
		// (~/.local itself also contains ~/.local/share/keyrings on Linux).
		{"contains own data", filepath.Join(home, ".local", "state"), "go clean -cache", "own_data"},
		{"protected home path", filepath.Join(home, ".ssh"), "true", "always_disallowed"},
		{"names never_touch", cache, "rm -rf ~/keep", "always_disallowed"},
		{"names never_touch via $HOME", cache, "rm -rf $HOME/KEEP/cache", "always_disallowed"},
	}
	for _, c := range cases {
		err := env.CheckDeletable(p, config.Entry{Path: c.path, Action: config.ActionCommand, Command: c.cmd})
		if err == nil || !strings.HasPrefix(err.Error(), c.rule+":") {
			t.Errorf("%s: want %s refusal, got %v", c.name, c.rule, err)
		}
	}
	if err := env.CheckDeletable(p, config.Entry{Path: cache, Action: config.ActionCommand, Command: "go clean -cache"}); err != nil {
		t.Fatalf("an ordinary command entry must pass: %v", err)
	}
}

// Every shipped default entry that can act must still pass the guards after
// the built-in list grew, apart from the command entries the path rules now
// refuse on purpose.
func TestShippedDefaultEntriesPassGuard(t *testing.T) {
	expectRefused := map[string]map[string]string{
		"darwin": {
			"/Library/Developer/CoreSimulator":                       "home",
			"~/Library/Containers/com.docker.docker/Data/vms/0/data": "never_touch",
		},
		"linux": {},
	}
	old := protect.Builtin
	t.Cleanup(func() { protect.Builtin = old })
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			protect.Builtin = protect.BuiltinFor(goos)
			home := t.TempDir()
			cfg, err := config.Parse(config.DefaultFor(goos), home)
			if err != nil {
				t.Fatal(err)
			}
			// The temporary home lives under /var on macOS, which the linux
			// default lists in never_touch; a real home never sits there.
			var nt []string
			for _, d := range cfg.Policy.NeverTouch {
				if !config.IsUnder(home, d) {
					nt = append(nt, d)
				}
			}
			cfg.Policy.NeverTouch = nt
			env := Env{Home: home, Procs: testutil.NoProcs}
			n := 0
			for _, e := range cfg.Entries(nil) {
				if e.Action == config.ActionNever {
					continue
				}
				if e.IsFile {
					testutil.Write(t, e.Path, 1)
				} else if config.IsUnder(e.Path, home) {
					testutil.Write(t, filepath.Join(e.Path, "x"), 1)
				}
				raw := "~" + strings.TrimPrefix(e.Path, home)
				if !config.IsUnder(e.Path, home) {
					raw = e.Path
				}
				err := env.CheckDeletable(cfg.Policy, e)
				if rule, ok := expectRefused[goos][raw]; ok {
					if err == nil || !strings.HasPrefix(err.Error(), rule+":") {
						t.Errorf("%s: want %s refusal, got %v", raw, rule, err)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s (%s) must stay cleanable: %v", raw, e.Action, err)
				}
				n++
			}
			if n == 0 {
				t.Fatal("no default entry was checked")
			}
		})
	}
}

func TestNewestChangeLooksAtTheWholeSubtree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "archive")
	child := filepath.Join(dir, "env")
	testutil.Write(t, filepath.Join(child, "lib", "deep", "fresh.py"), 10)
	testutil.AgeTree(t, child, 48*time.Hour)
	// One file deep inside is new; aging the top alone used to hide it.
	testutil.Write(t, filepath.Join(child, "lib", "deep", "new.py"), 10)
	testutil.Age(t, filepath.Join(child, "lib", "deep"), 48*time.Hour)
	testutil.Age(t, filepath.Join(child, "lib"), 48*time.Hour)
	testutil.Age(t, child, 48*time.Hour)
	kids, err := ClassifyChildren(dir, 24*time.Hour, nil, time.Now())
	if err != nil || len(kids) != 1 {
		t.Fatal(kids, err)
	}
	if !strings.Contains(kids[0].Keep, "floor") {
		t.Fatalf("a child with a fresh file inside must be kept, got %q", kids[0].Keep)
	}

	// A future mtime is fresh, not ancient.
	testutil.AgeTree(t, child, 48*time.Hour)
	future := time.Now().Add(240 * time.Hour)
	if err := os.Chtimes(filepath.Join(child, "lib", "deep", "new.py"), future, future); err != nil {
		t.Fatal(err)
	}
	kids, _ = ClassifyChildren(dir, 24*time.Hour, nil, time.Now())
	if kids[0].Keep == "" {
		t.Fatal("a future mtime must keep the child")
	}

	// All old: a delete candidate, so the refusals above are not vacuous.
	testutil.AgeTree(t, child, 48*time.Hour)
	kids, _ = ClassifyChildren(dir, 24*time.Hour, nil, time.Now())
	if kids[0].Keep != "" {
		t.Fatalf("old subtree should be stale, kept %q", kids[0].Keep)
	}

	// Over budget or out of work: kept, never guessed stale.
	old := StaleWalkMax
	StaleWalkMax = 2
	kids, _ = ClassifyChildren(dir, 24*time.Hour, nil, time.Now())
	StaleWalkMax = old
	if !strings.Contains(kids[0].Keep, "staleness unknown") {
		t.Fatalf("an unfinished walk must keep the child, got %q", kids[0].Keep)
	}
	ctx := worklimit.With(context.Background(), 1, 0)
	if _, err := NewestChange(ctx, child); err == nil {
		t.Fatal("exhausted work budget must be an error")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewestChange(cancelled, child); err == nil {
		t.Fatal("cancelled context must be an error")
	}
}

func TestNewestChangeCountsCtime(t *testing.T) {
	old := StaleUsesChangeTime
	StaleUsesChangeTime = true
	t.Cleanup(func() { StaleUsesChangeTime = old })
	dir := filepath.Join(t.TempDir(), "archive")
	child := filepath.Join(dir, "extracted")
	testutil.Write(t, filepath.Join(child, "f"), 10)
	// Archive extraction: brand-new files carrying old mtimes.
	testutil.AgeTree(t, child, 30*24*time.Hour)
	kids, err := ClassifyChildren(dir, 24*time.Hour, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if kids[0].Keep == "" {
		t.Fatal("files that just arrived with old mtimes must be kept by ctime")
	}
}

func TestRemovalRefusesOwnData(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	state := filepath.Join(home, ".local", "state", "oos")
	p.QuarantineDir = filepath.Join(state, "quarantine")
	p.StateFile = filepath.Join(state, "bigfile.json")
	p.LogFile = filepath.Join(home, "logs", "oos.log")
	testutil.Write(t, filepath.Join(p.QuarantineDir, "b1", "x"), 1)
	testutil.Write(t, p.StateFile, 1)
	testutil.Write(t, p.LogFile, 1)
	testutil.Write(t, filepath.Join(state, "mutation.lock"), 0)
	for _, path := range []string{
		p.QuarantineDir,
		filepath.Join(p.QuarantineDir, "b1", "x"),
		state,
		p.StateFile,
		p.StateFile + ".quarantine-index.json",
		filepath.Join(home, "logs"),
		filepath.Join(state, "mutation.lock"),
		filepath.Join(home, ".local", "state"),
	} {
		if err := CheckRemovalPath(p, path, home); err == nil || !strings.HasPrefix(err.Error(), "own_data:") {
			t.Errorf("%s: want own_data refusal, got %v", path, err)
		}
	}
	// ~/.local also contains protected paths on some platforms; the point
	// is only that it is refused.
	if err := CheckRemovalPath(p, filepath.Join(home, ".local"), home); err == nil {
		t.Error("~/.local contains oos's own data and must be refused")
	}
	// A case alias of the quarantine store (case-insensitive volumes).
	alias := filepath.Join(home, ".local", "state", "OOS", "Quarantine")
	if _, err := os.Stat(alias); err == nil {
		if err := CheckRemovalPath(p, alias, home); err == nil {
			t.Error("case alias of the quarantine store accepted")
		}
	}
	other := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(other, "f"), 1)
	if err := CheckRemovalPath(p, other, home); err != nil {
		t.Fatalf("unrelated path refused: %v", err)
	}
}

func TestInstallCheckFailsClosed(t *testing.T) {
	busy := Env{Procs: func() ([]string, error) { return []string{"/usr/bin/apt-get install -y x"}, nil }}
	if err := busy.CheckInstall(); err == nil || !strings.HasPrefix(err.Error(), "install:") {
		t.Fatalf("running apt-get must freeze: %v", err)
	}
	broken := Env{Procs: func() ([]string, error) { return nil, os.ErrPermission }}
	if err := broken.CheckInstall(); err == nil {
		t.Fatal("an unreadable process list must freeze")
	}
}

func TestRemovalHonoursBuiltinExceptions(t *testing.T) {
	// CheckRemovalPath checks real ancestors before the rules, so this needs
	// a Linux host where /root and /var/lib are there and readable.
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("needs linux as root")
	}
	for _, d := range []string{"/root/.cache", "/root/.npm", "/root/backups", "/var/lib/docker"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Skip(err)
		}
	}
	old := protect.Builtin
	t.Cleanup(func() { protect.Builtin = old })
	protect.Builtin = protect.BuiltinFor("linux")
	for path, allowed := range map[string]bool{
		"/root/.cache/pip":        true,
		"/root/.npm/_cacache":     true,
		"/root/.cache-evil":       false,
		"/root/backups/x":         false,
		"/root":                   false,
		"/var/lib/docker":         false, // excepted itself, but contains protected volumes/overlay2
		"/var/lib/docker/volumes": false,
		"/var/lib/vz":             false,
		"/var/lib/tailscale":      false,
	} {
		err := CheckRemovalPath(config.Policy{}, path, "/root")
		var r *Refusal
		protected := errors.As(err, &r) && r.Rule == "always_disallowed"
		if allowed && protected {
			t.Errorf("%s should not be protected: %v", path, err)
		}
		if !allowed && !protected {
			t.Errorf("%s must be refused as protected, got %v", path, err)
		}
	}
	// A config-added entry is never lifted by a built-in exception.
	var r *Refusal
	if err := CheckRemovalPath(config.Policy{AlwaysDisallowed: []string{"/root/.cache"}}, "/root/.cache/pip", "/root"); !errors.As(err, &r) || r.Rule != "always_disallowed" {
		t.Error("policy.always_disallowed must win over a built-in exception")
	}
}

func TestCommandPathJudgesOnlyThePathItself(t *testing.T) {
	home := t.TempDir()
	anchor := filepath.Join(home, "svc")
	protected := filepath.Join(anchor, "volumes")
	nt := filepath.Join(anchor, "db")
	for _, d := range []string{protected, nt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := config.Policy{AlwaysDisallowed: []string{protected}, NeverTouch: []string{nt}}
	// A removal of the anchor would take the protected children with it.
	if err := CheckRemovalPath(p, anchor, home); err == nil {
		t.Fatal("removing a directory that holds a protected path must be refused")
	}
	// A command filed under the anchor does not remove it: a protected path
	// below is no reason to refuse, but never_touch still counts both ways.
	if err := CheckCommandPath(config.Policy{AlwaysDisallowed: []string{protected}}, anchor, home); err != nil {
		t.Fatalf("a command anchored above protected paths must pass: %v", err)
	}
	if err := CheckCommandPath(p, anchor, home); err == nil {
		t.Fatal("a command anchored above a never_touch path must still be refused")
	}
	// A command filed at or under a protected or never_touch path is refused.
	for _, path := range []string{protected, filepath.Join(protected, "x"), nt, filepath.Join(nt, "x")} {
		if err := CheckCommandPath(p, path, home); err == nil {
			t.Errorf("command under %s must be refused", path)
		}
	}
	// And under oos's own data.
	own := config.Policy{StateFile: filepath.Join(home, "state", "bigfile.json")}
	if err := CheckCommandPath(own, filepath.Join(home, "state", "bigfile.json"), home); err == nil {
		t.Error("a command filed on oos's own state must be refused")
	}
}

func TestDockerBuilderPruneAnchorIsAllowedOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("needs linux as root")
	}
	old := protect.Builtin
	t.Cleanup(func() { protect.Builtin = old })
	protect.Builtin = protect.BuiltinFor("linux")
	if err := os.MkdirAll("/var/lib/docker/volumes", 0o710); err != nil {
		t.Skip(err)
	}
	p := config.Policy{AllowOutsideHome: true, AllowCommands: true}
	env := Env{Home: "/root", Procs: testutil.NoProcs}
	ent := config.Entry{Path: "/var/lib/docker", Action: config.ActionCommand, Command: "docker builder prune -f"}
	if err := env.CheckDeletable(p, ent); err != nil {
		t.Fatalf("docker builder prune anchored at /var/lib/docker must be allowed: %v", err)
	}
	// Emptying it is still refused.
	ent = config.Entry{Path: "/var/lib/docker", Action: config.ActionRmContents}
	if err := env.CheckDeletable(p, ent); err == nil {
		t.Fatal("rm-contents /var/lib/docker must stay refused")
	}
	// A command that names the volumes is refused by the text scan.
	ent = config.Entry{Path: "/var/lib/docker", Action: config.ActionCommand, Command: "rm -rf /var/lib/docker/volumes/x"}
	if err := env.CheckDeletable(p, ent); err == nil {
		t.Fatal("a command naming /var/lib/docker/volumes must be refused")
	}
}
