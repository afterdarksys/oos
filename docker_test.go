package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseDockerSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"19.72GB", 19_720_000_000, false},
		{"155.8MB", 155_800_000, false},
		{"1.093kB", 1093, false},
		{"298B", 298, false},
		{"0B", 0, false},
		{"15.21GB (77%)", 15_210_000_000, false},
		{"4.238GB", 4_238_000_000, false},
		{"1.5GiB", 1610612736, false},
		{" 2MB ", 2_000_000, false},
		{"N/A", -1, true},
		{"", -1, true},
		{"lots", -1, true},
		{"3XB", -1, true},
	}
	for _, c := range cases {
		got, err := parseDockerSize(c.in)
		if (err != nil) != c.err {
			t.Errorf("%q: err=%v want err=%v", c.in, err, c.err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %d want %d", c.in, got, c.want)
		}
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseDockerDFFixture(t *testing.T) {
	u, err := parseDockerDF(fixture(t, "docker-df.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := DockerUsage{
		Images:     DockerTotals{Count: 71, Active: 16, Bytes: 19_720_000_000, Reclaimable: 15_210_000_000},
		Containers: DockerTotals{Count: 25, Active: 19, Bytes: 155_800_000, Reclaimable: 80_000_000},
		Volumes:    DockerTotals{Count: 62, Active: 36, Bytes: 2_287_000_000, Reclaimable: 49_860_000},
		BuildCache: DockerTotals{Count: 217, Active: 0, Bytes: 4_238_000_000, Reclaimable: 4_238_000_000},
	}
	for _, c := range []struct {
		name      string
		got, want DockerTotals
	}{
		{"images", u.Images, want.Images}, {"containers", u.Containers, want.Containers},
		{"volumes", u.Volumes, want.Volumes}, {"build cache", u.BuildCache, want.BuildCache},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, c.got, c.want)
		}
	}
	if _, err := parseDockerDF([]byte("")); err == nil {
		t.Error("empty output must be an error, not zero totals")
	}
	if _, err := parseDockerDF([]byte(`{"Type":"Images","Size":"N/A","Reclaimable":"0B"}`)); err == nil {
		t.Error("an unreadable size must be an error")
	}
	if _, err := parseDockerDF([]byte(`not json`)); err == nil {
		t.Error("garbage must be an error")
	}
}

func TestParseDanglingVolumesFixture(t *testing.T) {
	vols, err := parseDanglingVolumes(fixture(t, "docker-volumes-dangling.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 26 {
		t.Fatalf("got %d dangling volumes, want 26", len(vols))
	}
	names := map[string]bool{}
	for i, v := range vols {
		if v.Bytes != -1 {
			t.Errorf("%s: listing carries no size, want -1, got %d", v.Name, v.Bytes)
		}
		if !strings.HasPrefix(v.Mountpoint, "/var/lib/docker/volumes/") {
			t.Errorf("%s: mountpoint %q", v.Name, v.Mountpoint)
		}
		if i > 0 && vols[i-1].Name > v.Name {
			t.Errorf("not sorted by name at %d", i)
		}
		names[v.Name] = true
	}
	// the exact trap: an "unused" volume that is really data
	if !names["postfix-smtp_authentik-media"] {
		t.Error("postfix-smtp_authentik-media should be listed as dangling")
	}
	if vols, _ := parseDanglingVolumes(nil); len(vols) != 0 {
		t.Error("no output means no dangling volumes")
	}
}

// stubDocker replaces the docker CLI for one test.
func stubDocker(t *testing.T, fn func(args ...string) ([]byte, error)) {
	t.Helper()
	old := dockerCmd
	dockerCmd = func(_ time.Duration, args ...string) ([]byte, error) { return fn(args...) }
	t.Cleanup(func() { dockerCmd = old })
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

func TestCollectDockerSizesLocalMountpoints(t *testing.T) {
	home := t.TempDir()
	onHost := filepath.Join(home, "vol", "_data")
	write(t, filepath.Join(onHost, "blob"), 3<<20)
	vols := `{"Name":"tiny","Mountpoint":"` + filepath.Join(home, "missing") + `"}
{"Name":"big","Mountpoint":"` + onHost + `"}
{"Name":"elsewhere","Mountpoint":"/nonexistent/docker/volumes/x/_data"}
`
	fixtureDocker(t, []byte(vols))
	u, err := collectDocker(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Dangling) != 3 {
		t.Fatalf("got %d dangling, want 3", len(u.Dangling))
	}
	if u.Dangling[0].Name != "big" || u.Dangling[0].Bytes < 3<<20 {
		t.Errorf("largest sized volume first: %+v", u.Dangling[0])
	}
	for _, v := range u.Dangling[1:] {
		if v.Bytes != -1 {
			t.Errorf("%s: mountpoint not on this host must be -1, got %d", v.Name, v.Bytes)
		}
	}
	if u.Images.Count != 71 || u.Volumes.Reclaimable != 49_860_000 {
		t.Errorf("totals not carried: %+v", u)
	}
}

func TestCollectDockerFailsClosed(t *testing.T) {
	stubDocker(t, func(args ...string) ([]byte, error) { return nil, errors.New("Cannot connect to the Docker daemon") })
	if _, err := collectDocker(time.Second); err == nil || !strings.Contains(err.Error(), "Cannot connect") {
		t.Errorf("daemon failure must surface: %v", err)
	}
	df := fixture(t, "docker-df.jsonl")
	stubDocker(t, func(args ...string) ([]byte, error) {
		if args[0] == "system" {
			return df, nil
		}
		return nil, errors.New("volume ls broke")
	})
	if _, err := collectDocker(time.Second); err == nil || !strings.Contains(err.Error(), "volume ls") {
		t.Errorf("a failed volume listing must not pass as 'no dangling volumes': %v", err)
	}
}

func TestDockerWantedAndTimeout(t *testing.T) {
	on, off := true, false
	if w, f := dockerWanted(Policy{Docker: &on}); !w || !f {
		t.Error("docker: true must want and force")
	}
	if w, _ := dockerWanted(Policy{Docker: &off}); w {
		t.Error("docker: false must not want")
	}
	if _, f := dockerWanted(Policy{}); f {
		t.Error("unset docker must never be forced")
	}
	if d := dockerTimeout(Policy{}); d != defaultDockerTimeout {
		t.Errorf("default timeout %s", d)
	}
	if d := dockerTimeout(Policy{DockerTimeoutSeconds: 7}); d != 7*time.Second {
		t.Errorf("timeout %s", d)
	}
}

func checkCfg(home string, docker *bool) *Config {
	p := policyFor(home)
	p.Docker = docker
	return &Config{Version: 1, Volume: home, Policy: p, home: home}
}

func TestCheckDockerSection(t *testing.T) {
	home := t.TempDir()
	on := true
	cfg := checkCfg(home, &on)
	fixtureDocker(t, fixture(t, "docker-volumes-dangling.jsonl"))
	var out, errw bytes.Buffer
	doCheck(cfg, Env{Home: home, Procs: noProcs}, &opts{check: true}, time.Now(), &out, &errw)
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
	doCheck(cfg, Env{Home: home, Procs: noProcs}, &opts{check: true, verbose: true}, time.Now(), &out, &errw)
	if !strings.Contains(out.String(), "postfix-smtp_authentik-media") || strings.Contains(out.String(), "more; -v") {
		t.Errorf("verbose must list every dangling volume:\n%s", out.String())
	}

	out.Reset()
	doCheck(cfg, Env{Home: home, Procs: noProcs}, &opts{check: true, jsonOut: true}, time.Now(), &out, &errw)
	var j struct {
		Docker struct {
			Images   DockerTotals   `json:"images"`
			Dangling []DockerVolume `json:"dangling_volumes"`
			Error    string         `json:"error"`
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
	code := doCheck(cfg, Env{Home: home, Procs: noProcs}, &opts{check: true}, time.Now(), &out, &errw)
	if code != exitOK {
		t.Errorf("a docker failure must not change the disk status: exit %d", code)
	}
	if !strings.Contains(out.String(), "docker: skipped (docker system df: timed out after 2m0s)") {
		t.Errorf("skip reason missing:\n%s", out.String())
	}
	if !strings.Contains(errw.String(), "oos: docker:") {
		t.Errorf("docker: true is forced, failure belongs on stderr: %q", errw.String())
	}
	out.Reset()
	doCheck(cfg, Env{Home: home, Procs: noProcs}, &opts{check: true, jsonOut: true}, time.Now(), &out, &errw)
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
	doCheck(checkCfg(home, &off), Env{Home: home, Procs: noProcs}, &opts{check: true}, time.Now(), &out, &errw)
	if called || strings.Contains(out.String(), "docker") {
		t.Error("docker: false must not touch the daemon or print a section")
	}
	on := true
	doCheck(checkCfg(home, &on), Env{Home: home, Procs: noProcs}, &opts{check: true, quick: true}, time.Now(), &out, &errw)
	if called {
		t.Error("--quick must not ask the daemon; that is the whole point of quick")
	}
}

func TestConfigDockerFields(t *testing.T) {
	home := t.TempDir()
	base := `{"version":1,"volume":"~","policy":{"min_free_gb":1,"warn_free_gb":2,"require_yes":true,"max_delete_gb_per_run":1,
	  "min_path_depth":3,"log_file":"~/l","state_file":"~/s","big_file_min_mb":1,"scan_top_n":1,"docker":false,"docker_timeout_seconds":%d},
	  "known_dirs":[],"known_files":[]}`
	cfg, err := parseConfig([]byte(strings.Replace(base, "%d", "30", 1)), home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Policy.Docker == nil || *cfg.Policy.Docker || cfg.Policy.DockerTimeoutSeconds != 30 {
		t.Errorf("docker fields not read: %+v", cfg.Policy)
	}
	if _, err := parseConfig([]byte(strings.Replace(base, "%d", "-1", 1)), home); err == nil || !strings.Contains(err.Error(), "docker_timeout_seconds") {
		t.Errorf("negative timeout must be rejected: %v", err)
	}
}
