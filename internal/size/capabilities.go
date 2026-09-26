package size

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

type Capabilities struct {
	Filesystem  string   `json:"filesystem"`
	Identity    string   `json:"identity"`
	ReadOnly    bool     `json:"read_only"`
	ExtentProbe string   `json:"extent_probe"`
	Sharing     string   `json:"sharing"`
	Notes       []string `json:"notes,omitempty"`
}

// Diagnostics asks only read-only filesystem tools, with a bounded deadline.
// Raw output is diagnostic context, never input to a removal decision.
func Diagnostics(ctx context.Context, path string) (map[string]string, error) {
	c, err := Filesystem(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{"filesystem": c.Filesystem}
	queries := [][]string{{"lsof", "-nP", "+L1"}}
	switch c.Filesystem {
	case "btrfs":
		queries = append(queries, [][]string{{"btrfs", "filesystem", "usage", "-b", path}, {"btrfs", "qgroup", "show", "-reF", path}, {"btrfs", "subvolume", "show", path}}...)
	case "xfs":
		queries = append(queries, [][]string{{"xfs_quota", "-x", "-c", "report -p -b", path}}...)
	case "apfs":
		queries = append(queries, [][]string{{"diskutil", "info", path}, {"tmutil", "listlocalsnapshots", path}}...)
	}
	for _, q := range queries {
		child, cancel := context.WithTimeout(ctx, 5*time.Second)
		buf := &boundedOutput{}
		cmd := exec.CommandContext(child, q[0], q[1:]...)
		cmd.Stdout = buf
		cmd.Stderr = buf
		e := cmd.Run()
		b := buf.Bytes()
		cancel()
		key := fmt.Sprint(q)
		if len(b) > 65536 {
			b = b[:65536]
		}
		out[key] = string(b)
		if e != nil {
			out[key] += "\n" + e.Error()
		}
	}
	return out, nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remain := 65536 - b.Len()
	if remain > 0 {
		if len(p) > remain {
			p = p[:remain]
		}
		b.Buffer.Write(p)
	}
	return n, nil
}
