package docker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/testutil"
)

func checkCfg(home string, docker *bool) *config.Config {
	p := testutil.PolicyFor(home)
	p.Docker = docker
	return &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
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
	old := Cmd
	Cmd = func(_ time.Duration, args ...string) ([]byte, error) { return fn(args...) }
	t.Cleanup(func() { Cmd = old })
}

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
		got, err := ParseSize(c.in)
		if (err != nil) != c.err {
			t.Errorf("%q: err=%v want err=%v", c.in, err, c.err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %d want %d", c.in, got, c.want)
		}
	}
}

func TestParseDockerDFFixture(t *testing.T) {
	u, err := ParseDF(fixture(t, "docker-df.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{
		Images:     Totals{Count: 71, Active: 16, Bytes: 19_720_000_000, Reclaimable: 15_210_000_000},
		Containers: Totals{Count: 25, Active: 19, Bytes: 155_800_000, Reclaimable: 80_000_000},
		Volumes:    Totals{Count: 62, Active: 36, Bytes: 2_287_000_000, Reclaimable: 49_860_000},
		BuildCache: Totals{Count: 217, Active: 0, Bytes: 4_238_000_000, Reclaimable: 4_238_000_000},
	}
	for _, c := range []struct {
		name      string
		got, want Totals
	}{
		{"images", u.Images, want.Images}, {"containers", u.Containers, want.Containers},
		{"volumes", u.Volumes, want.Volumes}, {"build cache", u.BuildCache, want.BuildCache},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, c.got, c.want)
		}
	}
	if _, err := ParseDF([]byte("")); err == nil {
		t.Error("empty output must be an error, not zero totals")
	}
	if _, err := ParseDF([]byte(`{"Type":"Images","Size":"N/A","Reclaimable":"0B"}`)); err == nil {
		t.Error("an unreadable size must be an error")
	}
	if _, err := ParseDF([]byte(`not json`)); err == nil {
		t.Error("garbage must be an error")
	}
}

func TestParseDanglingVolumesFixture(t *testing.T) {
	vols, err := ParseDangling(fixture(t, "docker-volumes-dangling.jsonl"))
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
	if vols, _ := ParseDangling(nil); len(vols) != 0 {
		t.Error("no output means no dangling volumes")
	}
}

func TestCollectDockerSizesLocalMountpoints(t *testing.T) {
	home := t.TempDir()
	onHost := filepath.Join(home, "vol", "_data")
	testutil.Write(t, filepath.Join(onHost, "blob"), 3<<20)
	vols := `{"Name":"tiny","Mountpoint":"` + filepath.Join(home, "missing") + `"}
{"Name":"big","Mountpoint":"` + onHost + `"}
{"Name":"elsewhere","Mountpoint":"/nonexistent/docker/volumes/x/_data"}
`
	fixtureDocker(t, []byte(vols))
	u, err := Collect(time.Second)
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
	if _, err := Collect(time.Second); err == nil || !strings.Contains(err.Error(), "Cannot connect") {
		t.Errorf("daemon failure must surface: %v", err)
	}
	df := fixture(t, "docker-df.jsonl")
	stubDocker(t, func(args ...string) ([]byte, error) {
		if args[0] == "system" {
			return df, nil
		}
		return nil, errors.New("volume ls broke")
	})
	if _, err := Collect(time.Second); err == nil || !strings.Contains(err.Error(), "volume ls") {
		t.Errorf("a failed volume listing must not pass as 'no dangling volumes': %v", err)
	}
}

func TestDockerWantedAndTimeout(t *testing.T) {
	on, off := true, false
	if w, f := Wanted(config.Policy{Docker: &on}); !w || !f {
		t.Error("docker: true must want and force")
	}
	if w, _ := Wanted(config.Policy{Docker: &off}); w {
		t.Error("docker: false must not want")
	}
	if _, f := Wanted(config.Policy{}); f {
		t.Error("unset docker must never be forced")
	}
	if d := Timeout(config.Policy{}); d != DefaultTimeout {
		t.Errorf("default timeout %s", d)
	}
	if d := Timeout(config.Policy{DockerTimeoutSeconds: 7}); d != 7*time.Second {
		t.Errorf("timeout %s", d)
	}
}
