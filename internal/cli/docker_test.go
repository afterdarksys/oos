package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/docker"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/testutil"
)

func checkCfg(home string, docker *bool) *config.Config {
	p := testutil.PolicyFor(home)
	p.Docker = docker
	return &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "docker", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixtureDocker(t *testing.T, volumes []byte) {
	t.Helper()
	df := fixture(t, "docker-df.jsonl")
	stubDocker(t, func(args ...string) ([]byte, error) {
		switch strings.Join(args[:2], " ") {
		case "system df":
			return df, nil
		case "volume ls":
			return volumes, nil
		}
		return nil, errors.New("unexpected docker " + strings.Join(args, " "))
	})
}

// stubDocker replaces the docker CLI for one test.
func stubDocker(t *testing.T, fn func(args ...string) ([]byte, error)) {
	t.Helper()
	old := docker.Cmd
	docker.Cmd = func(_ time.Duration, args ...string) ([]byte, error) { return fn(args...) }
	t.Cleanup(func() { docker.Cmd = old })
}

func TestCheckDockerSection(t *testing.T) {
	home := t.TempDir()
	on := true
	cfg := checkCfg(home, &on)
	fixtureDocker(t, fixture(t, "docker-volumes-dangling.jsonl"))
	var out, errw bytes.Buffer
	doCheck(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, &opts{check: true}, time.Now(), &out, &errw)
	s := out.String()
	for _, want := range []string{
		"docker (", "images         71 total,  16 in use", "build cache   217 records",
		"volumes        62 total,  36 in use", "in 26 dangling", "REFUSED", "never runs docker volume prune",
		"(+21 more; -v lists all)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Count(s, "  ?  ") > 5 {
		t.Errorf("non-verbose should cap the dangling list at 5:\n%s", s)
	}
	if errw.Len() != 0 {
		t.Errorf("no stderr on success: %s", errw.String())
	}

	out.Reset()
	doCheck(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, &opts{check: true, verbose: true}, time.Now(), &out, &errw)
	if !strings.Contains(out.String(), "postfix-smtp_authentik-media") || strings.Contains(out.String(), "more; -v") {
		t.Errorf("verbose must list every dangling volume:\n%s", out.String())
	}

	out.Reset()
	doCheck(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, &opts{check: true, jsonOut: true}, time.Now(), &out, &errw)
	var j struct {
		Docker struct {
			Images   docker.Totals   `json:"images"`
			Dangling []docker.Volume `json:"dangling_volumes"`
			Error    string          `json:"error"`
		} `json:"docker"`
	}
	if err := json.Unmarshal(out.Bytes(), &j); err != nil {
		t.Fatal(err)
	}
	if j.Docker.Images.Count != 71 || len(j.Docker.Dangling) != 26 || j.Docker.Error != "" {
		t.Errorf("json docker section: %+v", j.Docker)
	}
}

func TestCheckDockerSkippedOnError(t *testing.T) {
	home := t.TempDir()
	on := true
	cfg := checkCfg(home, &on)
	stubDocker(t, func(args ...string) ([]byte, error) { return nil, errors.New("timed out after 2m0s") })
	var out, errw bytes.Buffer
	code := doCheck(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, &opts{check: true}, time.Now(), &out, &errw)
	if code != status.ExitOK {
		t.Errorf("a docker failure must not change the disk status: exit %d", code)
	}
	if !strings.Contains(out.String(), "docker: skipped (docker system df: timed out after 2m0s)") {
		t.Errorf("skip reason missing:\n%s", out.String())
	}
	if !strings.Contains(errw.String(), "oos: docker:") {
		t.Errorf("docker: true is forced, failure belongs on stderr: %q", errw.String())
	}
	out.Reset()
	doCheck(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, &opts{check: true, jsonOut: true}, time.Now(), &out, &errw)
	if !strings.Contains(out.String(), `"docker":{"error":"docker system df: timed out after 2m0s"}`) {
		t.Errorf("json must carry the error:\n%s", out.String())
	}
}

func TestCheckDockerOffAndQuick(t *testing.T) {
	home := t.TempDir()
	off := false
	called := false
	stubDocker(t, func(args ...string) ([]byte, error) { called = true; return nil, errors.New("no") })
	var out, errw bytes.Buffer
	doCheck(checkCfg(home, &off), guard.Env{Home: home, Procs: testutil.NoProcs}, &opts{check: true}, time.Now(), &out, &errw)
	if called || strings.Contains(out.String(), "docker") {
		t.Error("docker: false must not touch the daemon or print a section")
	}
	on := true
	doCheck(checkCfg(home, &on), guard.Env{Home: home, Procs: testutil.NoProcs}, &opts{check: true, quick: true}, time.Now(), &out, &errw)
	if called {
		t.Error("--quick must not ask the daemon; that is the whole point of quick")
	}
}
