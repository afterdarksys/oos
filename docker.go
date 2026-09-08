package main

// Docker-aware sizing. The daemon's data root is one opaque directory to a
// file walk (and on macOS it is inside a VM disk image), so the only honest
// numbers come from the daemon itself: `docker system df` for totals and
// `docker volume ls -f dangling=true` for the volumes nothing references.
// Dangling volumes are listed and refused, never pruned: on this fleet
// "unused" volumes have been live data with a one-character name difference
// from the volume a container mounts.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultDockerTimeout = 120 * time.Second

// DockerTotals is one row of `docker system df`.
type DockerTotals struct {
	Count       int   `json:"count"`
	Active      int   `json:"active"`
	Bytes       int64 `json:"bytes"`
	Reclaimable int64 `json:"reclaimable"`
}

// DockerVolume is a volume no container references. Bytes is -1 when the
// mountpoint is not on this host (Docker Desktop) or cannot be read.
type DockerVolume struct {
	Name       string `json:"name"`
	Mountpoint string `json:"mountpoint,omitempty"`
	Bytes      int64  `json:"bytes"`
}

// DockerUsage is what the daemon reports, plus the dangling volume list.
type DockerUsage struct {
	Images     DockerTotals   `json:"images"`
	Containers DockerTotals   `json:"containers"`
	Volumes    DockerTotals   `json:"volumes"`
	BuildCache DockerTotals   `json:"build_cache"`
	Dangling   []DockerVolume `json:"dangling_volumes"`
	Elapsed    time.Duration  `json:"-"`
}

// dockerCmd runs the docker CLI with a deadline. Tests replace it.
var dockerCmd = func(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var stderr bytes.Buffer
	c := exec.CommandContext(ctx, "docker", args...)
	c.Stderr = &stderr
	out, err := c.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(firstLine(msg))
	}
	return out, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// dockerWanted decides whether to ask the daemon at all: policy.docker true
// forces it (and a failure is reported), false disables it, unset means
// "when a docker binary is on the PATH".
func dockerWanted(p Policy) (want, forced bool) {
	if p.Docker != nil {
		return *p.Docker, *p.Docker
	}
	_, err := exec.LookPath("docker")
	return err == nil, false
}

func dockerTimeout(p Policy) time.Duration {
	if p.DockerTimeoutSeconds > 0 {
		return time.Duration(p.DockerTimeoutSeconds) * time.Second
	}
	return defaultDockerTimeout
}

// parseDockerSize reads the sizes docker prints: "19.72GB", "155.8MB",
// "1.093kB", "0B", also the binary "1.5GiB" form and a trailing " (77%)".
func parseDockerSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if s == "" || s == "N/A" {
		return -1, fmt.Errorf("no size in %q", s)
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	num, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return -1, fmt.Errorf("bad size %q", s)
	}
	unit := strings.ToLower(strings.TrimSpace(s[i:]))
	mult := map[string]float64{
		"b": 1, "": 1,
		"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12, "pb": 1e15,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40, "pib": 1 << 50,
	}
	m, ok := mult[unit]
	if !ok {
		return -1, fmt.Errorf("bad size unit in %q", s)
	}
	return int64(num*m + 0.5), nil
}

// parseDockerDF reads `docker system df --format '{{json .}}'`: one JSON
// object per line with Type, TotalCount, Active, Size, Reclaimable.
func parseDockerDF(b []byte) (*DockerUsage, error) {
	u := &DockerUsage{}
	seen := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var row struct {
			Type, TotalCount, Active, Size, Reclaimable string
		}
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("docker system df: %v in %q", err, firstLine(string(line)))
		}
		t := DockerTotals{}
		t.Count, _ = strconv.Atoi(row.TotalCount)
		t.Active, _ = strconv.Atoi(row.Active)
		var err error
		if t.Bytes, err = parseDockerSize(row.Size); err != nil {
			return nil, fmt.Errorf("docker system df %s: %v", row.Type, err)
		}
		if t.Reclaimable, err = parseDockerSize(row.Reclaimable); err != nil {
			return nil, fmt.Errorf("docker system df %s: %v", row.Type, err)
		}
		switch strings.ToLower(row.Type) {
		case "images":
			u.Images = t
		case "containers":
			u.Containers = t
		case "local volumes", "volumes":
			u.Volumes = t
		case "build cache":
			u.BuildCache = t
		default:
			continue
		}
		seen++
	}
	if seen == 0 {
		return nil, errors.New("docker system df printed no rows")
	}
	return u, nil
}

// parseDanglingVolumes reads `docker volume ls -f dangling=true --format
// '{{json .}}'`. Sizes are not in that listing; the caller measures what it can.
func parseDanglingVolumes(b []byte) ([]DockerVolume, error) {
	var vols []DockerVolume
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var row struct{ Name, Mountpoint string }
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("docker volume ls: %v", err)
		}
		if row.Name == "" {
			continue
		}
		vols = append(vols, DockerVolume{Name: row.Name, Mountpoint: row.Mountpoint, Bytes: -1})
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
	return vols, nil
}

// collectDocker asks the daemon and sizes dangling volumes whose mountpoint
// is a directory on this host. Any failure is returned, never fatal to the
// caller: sizing the rest of the disk does not depend on docker answering.
func collectDocker(timeout time.Duration) (*DockerUsage, error) {
	start := time.Now()
	out, err := dockerCmd(timeout, "system", "df", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker system df: %v", err)
	}
	u, err := parseDockerDF(out)
	if err != nil {
		return nil, err
	}
	left := timeout - time.Since(start)
	if left < 5*time.Second {
		left = 5 * time.Second
	}
	vout, err := dockerCmd(left, "volume", "ls", "-f", "dangling=true", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker volume ls: %v", err)
	}
	if u.Dangling, err = parseDanglingVolumes(vout); err != nil {
		return nil, err
	}
	for i := range u.Dangling {
		v := &u.Dangling[i]
		if v.Mountpoint == "" {
			continue
		}
		if fi, err := os.Lstat(v.Mountpoint); err == nil && fi.IsDir() {
			if b, err := pathSize(v.Mountpoint); err == nil {
				v.Bytes = b
			}
		}
	}
	sortDangling(u.Dangling)
	u.Elapsed = time.Since(start)
	return u, nil
}

// sortDangling orders largest first, unknown sizes last, ties by name.
func sortDangling(vols []DockerVolume) {
	sort.SliceStable(vols, func(i, j int) bool {
		a, b := vols[i], vols[j]
		if (a.Bytes < 0) != (b.Bytes < 0) {
			return a.Bytes >= 0
		}
		if a.Bytes != b.Bytes {
			return a.Bytes > b.Bytes
		}
		return a.Name < b.Name
	})
}

// dockerReport is the --check section: what the daemon holds, what each
// prune command would return, and the dangling volumes with the refusal
// spelled out. Verbose lists every dangling volume.
func dockerReport(out io.Writer, u *DockerUsage, verbose bool) {
	fmt.Fprintf(out, "docker (%s):\n", u.Elapsed.Round(100*time.Millisecond))
	fmt.Fprintf(out, "  images       %4d total, %3d in use   %9s   %9s unused      -> docker image prune (dangling) / -a (all unused)\n",
		u.Images.Count, u.Images.Active, human(u.Images.Bytes), human(u.Images.Reclaimable))
	fmt.Fprintf(out, "  containers   %4d total, %3d running  %9s   %9s stopped     -> review by hand; docker container prune removes every stopped one\n",
		u.Containers.Count, u.Containers.Active, human(u.Containers.Bytes), human(u.Containers.Reclaimable))
	fmt.Fprintf(out, "  build cache  %4d records            %9s   %9s reclaimable -> docker builder prune -f\n",
		u.BuildCache.Count, human(u.BuildCache.Bytes), human(u.BuildCache.Reclaimable))
	fmt.Fprintf(out, "  volumes      %4d total, %3d in use   %9s   %9s in %d dangling\n",
		u.Volumes.Count, u.Volumes.Active, human(u.Volumes.Bytes), human(u.Volumes.Reclaimable), len(u.Dangling))
	if len(u.Dangling) == 0 {
		return
	}
	fmt.Fprintf(out, "  REFUSED: dangling volumes are data until a person says otherwise; oos never runs docker volume prune\n")
	limit := 5
	if verbose || len(u.Dangling) <= limit+1 {
		limit = len(u.Dangling)
	}
	for i, v := range u.Dangling[:limit] {
		size := "?"
		if v.Bytes >= 0 {
			size = human(v.Bytes)
		}
		label := "          "
		if i == 0 {
			label = "  dangling"
		}
		fmt.Fprintf(out, "  %s  %9s  %s\n", label, size, v.Name)
	}
	if rest := len(u.Dangling) - limit; rest > 0 {
		fmt.Fprintf(out, "              (+%d more; -v lists all)\n", rest)
	}
}

// dockerJSON is the --json shape for the docker section; err is carried
// as a string so a scripted caller sees why the section is missing.
func dockerJSON(u *DockerUsage, err error) map[string]any {
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{
		"images": u.Images, "containers": u.Containers, "volumes": u.Volumes, "build_cache": u.BuildCache,
		"dangling_volumes": u.Dangling, "elapsed_ms": u.Elapsed.Milliseconds(),
	}
}
