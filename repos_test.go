package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitRepo makes a real repository with one commit dated when.
func gitRepo(t *testing.T, dir string, when time.Time) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_AUTHOR_DATE="+when.Format(time.RFC3339), "GIT_COMMITTER_DATE="+when.Format(time.RFC3339),
			"HOME="+dir, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	write(t, filepath.Join(dir, "README"), 100)
	run("add", "README")
	run("commit", "-q", "-m", "init")
}

func TestBuildDirKind(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "rs", "Cargo.toml"), 1)
	write(t, filepath.Join(home, "js", "package.json"), 1)
	write(t, filepath.Join(home, "py", ".venv", "pyvenv.cfg"), 1)
	write(t, filepath.Join(home, "plain", "x"), 1)
	cases := []struct {
		parent, name, kind string
	}{
		{"rs", "target", "Rust target"},
		{"js", "node_modules", "node_modules"},
		{"js", ".next", ".next cache"},
		{"js", "dist", "dist output"},
		{"py", ".venv", "Python venv"},
		{"plain", "target", ""},       // no Cargo.toml beside it
		{"plain", "node_modules", ""}, // no package.json
		{"plain", ".venv", ""},        // no pyvenv.cfg inside
		{"plain", ".cache", "cache"},
		{"plain", "__pycache__", "Python cache"},
		{"plain", "src", ""},
	}
	for _, c := range cases {
		kind, _ := buildDirKind(filepath.Join(home, c.parent), c.name)
		if kind != c.kind {
			t.Errorf("%s/%s: got %q want %q", c.parent, c.name, kind, c.kind)
		}
	}
}

func TestScanBuildsReport(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	dev := filepath.Join(home, "development")

	// idle, clean Rust repo with a fat target: the suggestion case
	idle := filepath.Join(dev, "idle-rs")
	gitRepo(t, idle, now.Add(-90*24*time.Hour))
	write(t, filepath.Join(idle, "Cargo.toml"), 10)
	write(t, filepath.Join(idle, "src", "main.rs"), 200)
	write(t, filepath.Join(idle, "target", "debug", "bin"), 4<<20)
	for _, p := range []string{"Cargo.toml", "src/main.rs", "README", "src", ""} {
		age(t, filepath.Join(idle, p), 90*24*time.Hour)
	}
	// commit Cargo.toml and src so the repo is clean
	c := exec.Command("git", "-C", idle, "add", "-A")
	c.Env = append(os.Environ(), "HOME="+idle, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git add: %s", out)
	}
	c = exec.Command("git", "-C", idle, "commit", "-q", "-m", "src")
	old := now.Add(-80 * 24 * time.Hour).Format(time.RFC3339)
	c.Env = append(os.Environ(), "HOME="+idle, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_AUTHOR_DATE="+old, "GIT_COMMITTER_DATE="+old)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %s", out)
	}
	// git add/commit touched .git but not the source files; target is ignored? no .gitignore, so ignore it explicitly
	if err := os.WriteFile(filepath.Join(idle, ".git", "info", "exclude"), []byte("target\nvendor-fork\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// dirty JS repo: node_modules kept because of uncommitted work
	dirty := filepath.Join(dev, "dirty-js")
	gitRepo(t, dirty, now.Add(-10*24*time.Hour))
	write(t, filepath.Join(dirty, "package.json"), 10) // untracked = dirty
	write(t, filepath.Join(dirty, "node_modules", "left-pad", "index.js"), 2<<20)

	// a nested repo inside the Rust repo, and a plain dir with no .git
	nested := filepath.Join(idle, "vendor-fork")
	gitRepo(t, nested, now.Add(-5*24*time.Hour))
	write(t, filepath.Join(dev, "not-a-repo", "big"), 3<<20)

	cfg := &Config{Version: 1, Volume: home, Policy: policyFor(home), home: home}
	env := Env{Home: home}
	var out, errw bytes.Buffer
	if code := doScanBuilds(cfg, env, &opts{scanBuilds: dev, jsonOut: true, minMB: 1}, now, &out, &errw); code != exitOK {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
	var j struct {
		Repos []RepoRow `json:"repos"`
	}
	if err := json.Unmarshal(out.Bytes(), &j); err != nil {
		t.Fatal(err)
	}
	by := map[string]RepoRow{}
	for _, r := range j.Repos {
		by[r.Name] = r
	}
	if len(j.Repos) != 3 {
		t.Fatalf("want idle-rs, dirty-js and the nested repo; got %d: %+v", len(j.Repos), j.Repos)
	}
	rs := by["idle-rs"]
	if rs.Build < 4<<20 || len(rs.Builds) != 1 || rs.Builds[0].Kind != "Rust target" {
		t.Errorf("idle-rs builds: %+v", rs.Builds)
	}
	if !rs.DirtyKnown || rs.Dirty {
		t.Errorf("idle-rs should be clean: known=%v dirty=%v err=%s", rs.DirtyKnown, rs.Dirty, rs.Err)
	}
	if !rs.Builds[0].Suggest {
		t.Errorf("idle clean repo's target must be suggested: why=%q newest=%s commit=%s", rs.Builds[0].Why, rs.Newest, rs.LastCommit)
	}
	if len(rs.Nested) != 1 || filepath.Base(rs.Nested[0]) != "vendor-fork" {
		t.Errorf("nested repo not recorded: %v", rs.Nested)
	}
	if rs.Source > 1<<20 {
		t.Errorf("source must exclude target, .git and the nested repo: %d", rs.Source)
	}
	if rs.Git == 0 {
		t.Error(".git not sized")
	}
	js := by["dirty-js"]
	if !js.Dirty || len(js.Builds) != 1 || js.Builds[0].Suggest || !strings.Contains(js.Builds[0].Why, "uncommitted") {
		t.Errorf("dirty repo must keep node_modules: %+v", js.Builds)
	}
	if _, ok := by["not-a-repo"]; ok {
		t.Error("a plain directory is not a repo")
	}
	// default floor: a 4 MB target is under 50 MB and is not suggested
	out.Reset()
	doScanBuilds(cfg, env, &opts{scanBuilds: dev, jsonOut: true}, now, &out, &errw)
	var k struct {
		Repos []RepoRow `json:"repos"`
	}
	_ = json.Unmarshal(out.Bytes(), &k)
	for _, r := range k.Repos {
		for _, b := range r.Builds {
			if b.Suggest || !strings.Contains(b.Why, "under") && b.Bytes > 0 && b.Bytes < defaultBuildFloor && r.Name == "idle-rs" {
				t.Errorf("%s: %d bytes should sit under the floor: suggest=%v why=%q", b.Path, b.Bytes, b.Suggest, b.Why)
			}
		}
	}

	out.Reset()
	if code := doScanBuilds(cfg, env, &opts{scanBuilds: dev, verbose: true, minMB: 1}, now, &out, &errw); code != exitOK {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
	s := out.String()
	for _, want := range []string{"3 found", "suggest", "Rust target", "repo has uncommitted changes", "oos --add \"~/development/idle-rs/target\"", "--tags build-output,repo:idle-rs", "nothing was changed"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	lines := strings.Split(s, "\n")
	if !strings.Contains(lines[2], "idle-rs") {
		t.Errorf("largest build output first:\n%s", s)
	}

	// --older-than raises the idle bar past the repo's age: nothing suggested
	out.Reset()
	doScanBuilds(cfg, env, &opts{scanBuilds: dev, olderThan: "1y", verbose: true, minMB: 1}, now, &out, &errw)
	if strings.Contains(out.String(), "oos --add") || !strings.Contains(out.String(), "source changed 89d ago") {
		t.Errorf("idle 1y must refuse the 80-day-old repo:\n%s", out.String())
	}

	// git failing is not a suggestion
	oldGit := gitOut
	gitOut = func(dir string, args ...string) ([]byte, error) { return nil, errors.New("git: not found") }
	t.Cleanup(func() { gitOut = oldGit })
	out.Reset()
	doScanBuilds(cfg, env, &opts{scanBuilds: dev, jsonOut: true, minMB: 1}, now, &out, &errw)
	_ = json.Unmarshal(out.Bytes(), &j)
	for _, r := range j.Repos {
		for _, b := range r.Builds {
			if b.Suggest {
				t.Errorf("%s: suggested without git's word", b.Path)
			}
		}
	}
	if code := doScanBuilds(cfg, env, &opts{scanBuilds: filepath.Join(home, "nope")}, now, &out, &errw); code != exitUsage {
		t.Error("missing root must be a usage error")
	}
}

func TestFindReposDepthAndSkips(t *testing.T) {
	home := t.TempDir()
	mk := func(rel string) {
		if err := os.MkdirAll(filepath.Join(home, rel, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("a")
	mk("b/c/d/e")     // depth 4
	mk("b/c/d/e/f/g") // depth 6, beyond default
	mk("a/node_modules/pkg")
	write(t, filepath.Join(home, "a", "package.json"), 1)
	mk(".hidden/repo")
	repos, err := findRepos(home, 4)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(repos, "\n")
	if !strings.Contains(got, filepath.Join(home, "a")) || !strings.Contains(got, filepath.Join(home, "b/c/d/e")) {
		t.Errorf("missing repos: %s", got)
	}
	if strings.Contains(got, "f/g") || strings.Contains(got, "node_modules") || strings.Contains(got, ".hidden") {
		t.Errorf("should not descend past depth, into build dirs or hidden dirs: %s", got)
	}
}
