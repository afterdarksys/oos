package cli

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/agent"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/mutation"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/testutil"
)

// jsonLines decodes every line of out as a JSON object; any non-JSON line
// (text leaking into -j output) fails the test.
func jsonLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var docs []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("non-JSON line in -j output: %q (%v)\nall:\n%s", sc.Text(), err, out)
		}
		docs = append(docs, m)
	}
	return docs
}

func TestExitCodesForMutationErrors(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, status.ExitOK},
		{fmt.Errorf("x: %w", mutation.ErrBusy), status.ExitBusy},
		{fmt.Errorf("x: %w", plan.ErrPartial), status.ExitPartial},
		{errors.New("disk on fire"), status.ExitIO},
	}
	for _, c := range cases {
		if got := exitFor(c.err, status.ExitIO); got != c.want {
			t.Errorf("exitFor(%v) = %d, want %d", c.err, got, c.want)
		}
	}
	for code, kind := range map[int]string{4: "busy", 5: "partial", 6: "io", 3: "usage", 2: "critical"} {
		if status.Kind(code) != kind {
			t.Errorf("kind %d = %q", code, status.Kind(code))
		}
	}
}

// writeHomeConfig puts a config in a temp HOME for end-to-end Run tests.
func writeHomeConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(agent.JitterEnv, "")
	p := filepath.Join(home, ".config", "oos", "oos.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func futureConfig() string {
	return strings.Replace(string(config.Default), `"version": 1,`, `"version": 1, "future_knob": 1,`, 1)
}

func TestRunBusyLockExits4WithJSON(t *testing.T) {
	home := writeHomeConfig(t, string(config.Default))
	cfg, _, err := config.Load("", home)
	if err != nil {
		t.Fatal(err)
	}
	l, err := plan.Mutation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var out, errw bytes.Buffer
	code := Run([]string{"-C", "-y", "-j"}, &out, &errw)
	if code != status.ExitBusy {
		t.Fatalf("busy lock must exit %d, got %d: %s", status.ExitBusy, code, errw.String())
	}
	docs := jsonLines(t, out.String())
	if len(docs) != 1 || docs[0]["kind"] != "cleanup" || docs[0]["error_kind"] != "busy" {
		t.Fatalf("one busy JSON document: %v", docs)
	}
}

func TestRunAgentTickFailsLoudlyOnRejectedConfig(t *testing.T) {
	writeHomeConfig(t, futureConfig())
	var notes []string
	old := agent.Notify
	agent.Notify = func(title, msg string) error { notes = append(notes, msg); return nil }
	defer func() { agent.Notify = old }()
	var out, errw bytes.Buffer
	code := Run([]string{"--agent-tick", "-j"}, &out, &errw)
	if code == status.ExitOK {
		t.Fatal("a rejected config must not exit 0")
	}
	docs := jsonLines(t, out.String())
	if len(docs) != 1 || !strings.Contains(fmt.Sprint(docs[0]["config_error"]), "future_knob") || len(notes) != 1 {
		t.Fatalf("docs=%v notes=%v err=%s", docs, notes, errw.String())
	}
	Run([]string{"--agent-tick"}, &out, &errw)
	if len(notes) != 1 {
		t.Fatalf("rate-limited to one notification a day: %v", notes)
	}
}

func TestRunCLIStillRejectsUnknownKeys(t *testing.T) {
	writeHomeConfig(t, futureConfig())
	var out, errw bytes.Buffer
	if code := Run([]string{"-c", "-q"}, &out, &errw); code != status.ExitUsage {
		t.Fatalf("typos fail loudly in the CLI: %d", code)
	}
	if !strings.Contains(errw.String(), `unknown key "future_knob"`) || !strings.Contains(errw.String(), "older than the config") {
		t.Errorf("message must name the key and the likely cause: %s", errw.String())
	}
}

func TestRunStatusDegradesAndShowsConfigError(t *testing.T) {
	writeHomeConfig(t, futureConfig())
	var out, errw bytes.Buffer
	code := Run([]string{"--status", "-j"}, &out, &errw)
	if code == status.ExitUsage {
		t.Fatalf("--status must still answer on a rejected config: %s", errw.String())
	}
	docs := jsonLines(t, out.String())
	if len(docs) != 1 || docs[0]["kind"] != "status" || !strings.Contains(fmt.Sprint(docs[0]["config_error"]), "future_knob") {
		t.Fatalf("status json: %v", docs)
	}
}

func TestPurgeAndRestoreJSON(t *testing.T) {
	home := t.TempDir()
	p := quarantinePolicy(home)
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	now := time.Now()
	var out, errw bytes.Buffer
	if code := doPurge(cfg, &opts{purge: true, jsonOut: true}, now, &out, &errw); code != status.ExitOK {
		t.Fatalf("dry-run purge: %d %s", code, errw.String())
	}
	docs := jsonLines(t, out.String())
	if len(docs) != 1 || docs[0]["kind"] != "purge" || docs[0]["live"] != false {
		t.Fatalf("purge dry-run json: %v", docs)
	}
	out.Reset()
	if code := doPurge(cfg, &opts{purgeNow: true, yes: true, jsonOut: true}, now, &out, &errw); code != status.ExitOK {
		t.Fatalf("live purge: %d %s", code, errw.String())
	}
	docs = jsonLines(t, out.String())
	if len(docs) != 1 || docs[0]["live"] != true || docs[0]["purged"] == nil {
		t.Fatalf("purge live json: %v", docs)
	}
	out.Reset()
	errw.Reset()
	code := doRestore(cfg, &opts{restore: "20000101-000000", jsonOut: true}, &out, &errw)
	if code == status.ExitOK {
		t.Fatal("a missing batch is an error")
	}
	docs = jsonLines(t, out.String())
	if len(docs) != 1 || docs[0]["kind"] != "restore" || docs[0]["error"] == nil || errw.Len() == 0 {
		t.Fatalf("restore error: JSON on stdout, text on stderr: %v / %s", docs, errw.String())
	}
	out.Reset()
	cfg.Policy.Quarantine = false
	doPurge(cfg, &opts{purge: true, jsonOut: true}, now, &out, &errw)
	if docs = jsonLines(t, out.String()); len(docs) != 1 || docs[0]["error_kind"] != "usage" {
		t.Fatalf("errors in -j mode are JSON: %v", docs)
	}
}

func TestCleanupJSONHasKindAndRefusedList(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 10)
	never := filepath.Join(home, "a", "keep")
	testutil.Write(t, filepath.Join(never, "f"), 10)
	cfg := &config.Config{Version: 1, Volume: home, Policy: testutil.PolicyFor(home), Home: home,
		KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmContents}, {Path: never, Type: "cache", Action: config.ActionNever}}}
	var out, errw bytes.Buffer
	doCleanup(cfg, guard.Env{Home: home, Procs: testutil.NoProcs}, &opts{cleanup: true, jsonOut: true}, time.Now(), &out, &errw)
	docs := jsonLines(t, out.String())
	if len(docs) != 1 || docs[0]["kind"] != "cleanup" {
		t.Fatalf("cleanup json: %v", docs)
	}
	refused, _ := docs[0]["refused"].([]any)
	if len(refused) != 1 || refused[0].(map[string]any)["path"] != never {
		t.Fatalf("refused list: %v", docs[0]["refused"])
	}
}

func TestPathB64ForNonUTF8(t *testing.T) {
	bad := "/tmp/caf\xe9"
	m := setPath(map[string]any{}, bad)
	if m["path_b64"] != base64.StdEncoding.EncodeToString([]byte(bad)) {
		t.Fatalf("path_b64 missing: %v", m)
	}
	if m := setPath(map[string]any{}, "/tmp/café"); m["path_b64"] != nil {
		t.Fatalf("valid UTF-8 needs no path_b64: %v", m)
	}
	rows := planJSON([]plan.Item{{Entry: config.Entry{Path: bad}}})
	if rows[0]["path_b64"] == nil {
		t.Fatal("plan rows carry path_b64")
	}
}

func TestLogTailReadsFromTheEnd(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	if err := os.MkdirAll(filepath.Dir(p.LogFile), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 20000; i++ { // well past one 64 KiB chunk
		fmt.Fprintf(&b, "line %05d\n", i)
	}
	if err := os.WriteFile(p.LogFile, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	for _, n := range []int{1, 3, 7000, 30000} {
		var out, errw bytes.Buffer
		doLogTail(cfg, &opts{logTail: n}, &out, &errw)
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		want := n
		if want > 20000 {
			want = 20000
		}
		if len(lines) != want || lines[len(lines)-1] != "line 19999" || lines[0] != fmt.Sprintf("line %05d", 20000-want) {
			t.Fatalf("tail %d: %d lines, first %q last %q", n, len(lines), lines[0], lines[len(lines)-1])
		}
	}
}

func TestSkippedCommandsWarnOncePerHost(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	p.StateFile = filepath.Join(home, "state", "bigfile.json")
	p.AllowCommands = false
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home,
		KnownDirs: []config.Entry{{Path: filepath.Join(home, "vm"), Type: "vm", Action: config.ActionCommand, Command: "true"}}}
	var errw bytes.Buffer
	// the embedded default's own command entries are not the host's choice
	for _, src := range []string{"embedded default", "embedded default (config rejected)"} {
		warnSkippedCommands(cfg, src, &errw)
		if errw.Len() != 0 {
			t.Fatalf("%s warned: %q", src, errw.String())
		}
	}
	src := filepath.Join(home, ".config", "oos", "oos.json")
	warnSkippedCommands(cfg, src, &errw)
	if want := "oos: 1 command entries are skipped because allow_commands is false; set allow_commands: true to run them\n"; errw.String() != want {
		t.Fatalf("warning: %q", errw.String())
	}
	errw.Reset()
	warnSkippedCommands(cfg, src, &errw)
	if errw.Len() != 0 {
		t.Fatalf("once per host: %q", errw.String())
	}
	cfg.Policy.AllowCommands = true
	cfg.Policy.StateFile = filepath.Join(home, "other.json")
	warnSkippedCommands(cfg, src, &errw)
	if errw.Len() != 0 {
		t.Fatal("no warning when commands are allowed")
	}
}

func TestFleetParallelFlag(t *testing.T) {
	var errw bytes.Buffer
	if o, err := parseFlags([]string{"--fleet", "--fleet-parallel", "32"}, &errw); err != nil || o.fleetParallel != 32 {
		t.Fatalf("parse: %v %+v", err, o)
	}
	for _, bad := range []string{"0", "257", "-1"} {
		if _, err := parseFlags([]string{"--fleet", "--fleet-parallel", bad}, &errw); err == nil {
			t.Errorf("--fleet-parallel %s must be rejected", bad)
		}
	}
}

func TestDaemonLogNotDuplicatedWhenStdoutIsTheLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := daemonLog(path, f) // launchd: stdout is daemon.log itself
	fmt.Fprintln(w, "one line")
	if c, ok := w.(interface{ Close() error }); ok {
		c.Close()
	}
	b, _ := os.ReadFile(path)
	if strings.Count(string(b), "one line") != 1 {
		t.Fatalf("written %d times: %q", strings.Count(string(b), "one line"), b)
	}
	var buf bytes.Buffer
	w = daemonLog(path, &buf) // a terminal: both
	fmt.Fprintln(w, "two")
	if c, ok := w.(interface{ Close() error }); ok {
		c.Close()
	}
	b, _ = os.ReadFile(path)
	if buf.String() != "two\n" || !strings.Contains(string(b), "two") {
		t.Fatalf("stdout %q file %q", buf.String(), b)
	}
}
