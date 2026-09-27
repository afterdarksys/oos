package fleet

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/status"
)

func stub(t *testing.T, answers map[string]string, fail map[string]string) {
	t.Helper()
	old := SSH
	SSH = func(target string, timeout time.Duration, args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "-c -q -j" {
			return nil, errors.New("unexpected args " + strings.Join(args, " "))
		}
		if msg, ok := fail[target]; ok {
			return nil, errors.New(msg)
		}
		a, ok := answers[target]
		if !ok {
			return nil, errors.New("no such host")
		}
		return []byte(a), nil
	}
	t.Cleanup(func() { SSH = old })
}

func TestCollectReportAndWorst(t *testing.T) {
	stub(t, map[string]string{
		"relay-b": `{"version":"0.6.0","volume":"/","free_gb":187.4,"total_gb":295.3,"status":"OK","quarantine_bytes":0,"forecast":{"rate_gb_per_hour":0,"points":2,"note":"needs 3 readings inside 6.0h, have 2"}}`,
		"apps":    `{"version":"0.6.0","volume":"/","free_gb":12.0,"total_gb":492.3,"status":"WARN","quarantine_bytes":2147483648,"forecast":{"rate_gb_per_hour":-1.5,"window_hours":5,"points":6,"free_gb":12,"hours_to_critical":4.67}}`,
		"garbage": `not json`,
	}, map[string]string{"dr1": "ssh: connect to host dr1 port 22: Connection refused"})
	hosts := Collect([]string{"relay-b", "apps", "dr1", "garbage"}, time.Second)
	if len(hosts) != 4 {
		t.Fatalf("rows: %d", len(hosts))
	}
	if hosts[0].Target != "relay-b" || hosts[0].Status != "OK" || hosts[0].Version != "0.6.0" {
		t.Errorf("relay-b: %+v", hosts[0])
	}
	if hosts[1].Status != "WARN" || hosts[1].QuarantineBytes != 2147483648 || hosts[1].Forecast.HoursToCritical < 4.6 {
		t.Errorf("apps: %+v", hosts[1])
	}
	if hosts[2].Err == "" || !strings.Contains(hosts[2].Err, "Connection refused") || hosts[2].Status != "?" {
		t.Errorf("dr1 must be a row with the error: %+v", hosts[2])
	}
	if hosts[3].Err == "" || !strings.Contains(hosts[3].Err, "unreadable") {
		t.Errorf("garbage must be flagged: %+v", hosts[3])
	}
	if Worst(hosts) != status.ExitUsage {
		t.Errorf("an unreachable host is exit %d, got %d", status.ExitUsage, Worst(hosts))
	}
	if Worst(hosts[:2]) != status.ExitWarn {
		t.Errorf("worst of ok+warn is warn, got %d", Worst(hosts[:2]))
	}

	var out bytes.Buffer
	Report(&out, hosts, false)
	s := out.String()
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) != 5 {
		t.Fatalf("want header + 4 rows:\n%s", s)
	}
	if !strings.Contains(lines[1], "apps") || !strings.Contains(lines[1], "critical in 4.7h") || !strings.Contains(lines[1], "2.0 GB") {
		t.Errorf("least free first with forecast and quarantine:\n%s", s)
	}
	if !strings.Contains(lines[2], "relay-b") || !strings.Contains(lines[2], " ok ") {
		t.Errorf("relay-b second:\n%s", s)
	}
	if !strings.Contains(lines[4], "Connection refused") && !strings.Contains(lines[3], "Connection refused") {
		t.Errorf("unreachable rows last with the reason:\n%s", s)
	}
	out.Reset()
	Report(&out, hosts, true)
	if !strings.Contains(out.String(), "oos 0.6.0 on /") {
		t.Errorf("verbose adds version and volume:\n%s", out.String())
	}
}

func TestValidTargetRejectsOptionsAndJunk(t *testing.T) {
	for _, bad := range []string{"", "-oProxyCommand=touch /tmp/x", "-v", "a b", "host\n", "x\x00y"} {
		if ValidTarget(bad) == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	for _, good := range []string{"apps", "root@relay-b", "user@10.0.0.1", "h.example.com"} {
		if err := ValidTarget(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
	calls := 0
	old := SSH
	SSH = func(string, time.Duration, ...string) ([]byte, error) { calls++; return nil, errors.New("x") }
	defer func() { SSH = old }()
	hosts := Collect([]string{"-oProxyCommand=evil"}, time.Second)
	if calls != 0 || hosts[0].Err == "" {
		t.Fatalf("an option-shaped target must never reach ssh: calls=%d %+v", calls, hosts)
	}
	if _, err := old("-oProxyCommand=evil", time.Second); err == nil {
		t.Fatal("the real SSH must validate too")
	}
}

func TestHungHostCannotBlockCompletion(t *testing.T) {
	old := SSH
	release := make(chan struct{})
	SSH = func(target string, timeout time.Duration, args ...string) ([]byte, error) {
		if target == "hung" {
			<-release // ignores its timeout entirely
		}
		return []byte(`{"status":"OK","free_gb":1,"total_gb":2}`), nil
	}
	oldSlack := hardSlack
	hardSlack = 50 * time.Millisecond
	defer func() { SSH = old; hardSlack = oldSlack; close(release) }()
	start := time.Now()
	hosts := CollectN([]string{"ok1", "hung", "ok2"}, 10*time.Millisecond, 2)
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("took %s", el)
	}
	if hosts[1].Err == "" || hosts[0].Err != "" || hosts[2].Err != "" {
		t.Fatalf("rows: %+v", hosts)
	}
}

func TestCollectNClampsParallel(t *testing.T) {
	stub(t, map[string]string{"a": `{"status":"OK"}`}, nil)
	for _, n := range []int{-1, 0, 1, 100000} {
		if h := CollectN([]string{"a"}, time.Second, n); len(h) != 1 || h[0].Err != "" {
			t.Fatalf("parallel %d: %+v", n, h)
		}
	}
}
