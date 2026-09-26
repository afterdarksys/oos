package snapshots

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const listing = `Snapshots for volume group containing disk /:
com.apple.TimeMachine.2026-09-06-101010.local
com.apple.TimeMachine.2026-09-08-090000.local
com.apple.TimeMachine.2026-09-07-233000.local
garbage line
`

func TestParseAndCollect(t *testing.T) {
	snaps := Parse([]byte(listing))
	if len(snaps) != 3 || snaps[0].At.Day() != 6 || snaps[2].At.Day() != 8 {
		t.Fatalf("parse: %+v", snaps)
	}
	old := Cmd
	Cmd = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "listlocalsnapshots /" {
			return nil, errors.New("unexpected " + strings.Join(args, " "))
		}
		return []byte(listing), nil
	}
	t.Cleanup(func() { Cmd = old })
	r, err := Collect("/")
	if err != nil {
		t.Fatal(err)
	}
	if r.Count != 3 || r.Oldest.Day() != 6 || r.Newest.Day() != 8 || !strings.HasPrefix(r.Thin, "tmutil thinlocalsnapshots / ") {
		t.Errorf("report: %+v", r)
	}
	var out bytes.Buffer
	Print(&out, r, false)
	if !strings.Contains(out.String(), "local snapshots: 3 (oldest 2026-09-06") || !strings.Contains(out.String(), "release with: tmutil thinlocalsnapshots") {
		t.Errorf("print:\n%s", out.String())
	}
	out.Reset()
	Print(&out, r, true)
	if strings.Count(out.String(), "com.apple.TimeMachine.") != 3 {
		t.Errorf("verbose lists each snapshot:\n%s", out.String())
	}

	Cmd = func(args ...string) ([]byte, error) {
		return []byte("Snapshots for volume group containing disk /:\n"), nil
	}
	r, _ = Collect("/")
	out.Reset()
	Print(&out, r, false)
	if r.Count != 0 || !strings.Contains(out.String(), "none") {
		t.Errorf("no snapshots: %+v %s", r, out.String())
	}

	Cmd = func(args ...string) ([]byte, error) { return nil, errors.New("tmutil: not permitted") }
	if _, err := Collect("/"); err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("failure must surface: %v", err)
	}
}

func TestUpdateSnapshotsStayOutOfTheTimeMachineList(t *testing.T) {
	const raw = "com.apple.TimeMachine.2026-09-06-101010.local\ncom.apple.os.update-ABCDEF\ncom.apple.os.update-MSUPrepareUpdate\n"
	tm := Parse([]byte(raw))
	up := ParseUpdates([]byte(raw))
	if len(tm) != 1 || len(up) != 2 || up[0].Name != "com.apple.os.update-ABCDEF" {
		t.Fatalf("tm %+v up %+v", tm, up)
	}
	r := &Report{Updates: up, Note: "n", Thin: "tmutil thinlocalsnapshots / 1 4"}
	var out bytes.Buffer
	Print(&out, r, true)
	if !strings.Contains(out.String(), "os update snapshots: 2") || !strings.Contains(out.String(), "does not delete") || !strings.Contains(out.String(), "MSUPrepareUpdate") {
		t.Errorf("print:\n%s", out.String())
	}
}
