package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/mutation"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/status"
	"github.com/afterdarksys/oos/internal/testutil"
	"golang.org/x/sys/unix"
)

var (
	errBusy    = fmt.Errorf("store: %w", mutation.ErrBusy)
	errPartial = fmt.Errorf("%w: item b refused", plan.ErrPartial)
	errAudit   = fmt.Errorf("x: %w", plan.ErrAudit)
	errRefused = fmt.Errorf("x: %w", plan.ErrRefused)
	errOther   = errors.New("cannot verify processes: container PID namespace")
)

// TestRanExitTable: 5 only when some work was done; a run where every item
// was refused is 2 "refused", not partial.
func TestRanExitTable(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		fallback int
		code     int
		kind     string
	}{
		{"ok", nil, status.ExitCritical, status.ExitOK, ""},
		{"busy", errBusy, status.ExitCritical, status.ExitBusy, "busy"},
		{"partial", errPartial, status.ExitCritical, status.ExitPartial, "partial"},
		{"audit log", errAudit, status.ExitCritical, status.ExitIO, "io"},
		{"partial beats audit", fmt.Errorf("%w: %w", plan.ErrPartial, errAudit), status.ExitCritical, status.ExitPartial, "partial"},
		{"all refused", errOther, status.ExitCritical, status.ExitCritical, "refused"},
		{"io fallback", errOther, status.ExitIO, status.ExitIO, "io"},
	}
	for _, c := range cases {
		code, kind := ranExit(c.err, c.fallback)
		if code != c.code || kind != c.kind {
			t.Errorf("%s: ranExit = %d %q, want %d %q", c.name, code, kind, c.code, c.kind)
		}
	}
}

func TestEnsureExitTable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		kind string
	}{
		{"ok", nil, status.ExitOK, ""},
		{"busy", errBusy, status.ExitBusy, "busy"},
		{"partial", errPartial, status.ExitPartial, "partial"},
		{"nothing done", errRefused, status.ExitCritical, "refused"},
		{"audit inside a refusal", fmt.Errorf("%w: %w", plan.ErrRefused, errAudit), status.ExitIO, "io"},
		{"before acting (statfs, log)", errOther, status.ExitIO, "io"},
	}
	for _, c := range cases {
		code, kind := ensureExit(c.err)
		if code != c.code || kind != c.kind {
			t.Errorf("%s: ensureExit = %d %q, want %d %q", c.name, code, kind, c.code, c.kind)
		}
	}
}

func TestPurgeExitTable(t *testing.T) {
	errLock := fmt.Errorf("%w: lock is a symlink", plan.ErrStoreLock)
	cases := []struct {
		name         string
		purged, held int
		err          error
		code         int
		kind         string
	}{
		{"ok", 2, 0, nil, status.ExitOK, ""},
		{"nothing to purge", 0, 0, nil, status.ExitOK, ""},
		{"every batch held", 0, 2, nil, status.ExitNothing, "nothing_actionable"},
		{"some purged, others held", 1, 1, nil, status.ExitOK, ""},
		{"delete failed, nothing purged", 0, 0, errors.New("batch held: cannot be deleted completely"), status.ExitCritical, "refused"},
		{"delete failed beside held", 0, 1, errors.New("batch held: cannot be deleted completely"), status.ExitCritical, "refused"},
		{"some purged, others failed", 1, 0, errPartial, status.ExitPartial, "partial"},
		{"some purged, unwrapped error", 1, 0, errors.New("held"), status.ExitPartial, "partial"},
		{"busy", 0, 0, errBusy, status.ExitBusy, "busy"},
		{"unsafe store lock", 0, 0, errLock, status.ExitIO, "io"},
	}
	for _, c := range cases {
		code, kind := purgeExit(c.purged, c.held, c.err)
		if code != c.code || kind != c.kind {
			t.Errorf("%s: purgeExit = %d %q, want %d %q", c.name, code, kind, c.code, c.kind)
		}
	}
}

// TestPlanRefusedOnlyTable: 7 needs at least one work entry and every work
// entry refused by policy; never entries and missing paths do not count.
func TestPlanRefusedOnlyTable(t *testing.T) {
	rm := config.Entry{Action: config.ActionRmContents}
	never := config.Entry{Action: config.ActionNever}
	cmd := config.Entry{Action: config.ActionCommand}
	policy := guard.Refuse("never_touch", "contains entry marked never")
	missing := guard.Refuse("missing", "path does not exist")
	cases := []struct {
		name  string
		items []plan.Item
		want  bool
	}{
		{"no entries", nil, false},
		{"all refused by policy", []plan.Item{{Entry: rm, Refused: policy}, {Entry: cmd, Refused: guard.Refuse("commands", "allow_commands is false")}}, true},
		{"refused beside never", []plan.Item{{Entry: rm, Refused: policy}, {Entry: never, Refused: guard.Refuse("action", "entry is marked never")}}, true},
		{"some refused, rest ok", []plan.Item{{Entry: rm, Refused: policy}, {Entry: rm}}, false},
		{"only never entries", []plan.Item{{Entry: never, Refused: guard.Refuse("action", "entry is marked never")}}, false},
		{"refused beside missing", []plan.Item{{Entry: rm, Refused: policy}, {Entry: rm, Refused: missing}}, false},
	}
	for _, c := range cases {
		if got := planRefusedOnly(c.items); got != c.want {
			t.Errorf("%s: planRefusedOnly = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestCleanupAllPlanRefusedExits7: a live cleanup whose every entry is
// refused by policy (here allow_commands false) did nothing it could: 7
// "nothing_actionable" in text and JSON. With one entry that runs, the
// refused one does not change the exit (0).
func TestCleanupAllPlanRefusedExits7(t *testing.T) {
	home := t.TempDir()
	cmdDir := filepath.Join(home, "a", "tool")
	testutil.Write(t, filepath.Join(cmdDir, "f"), 10)
	p := testutil.PolicyFor(home)
	p.AllowCommands = false
	p.MinFreeGB, p.WarnFreeGB = 0, 0 // the exit is the run, not the test disk
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home,
		KnownDirs: []config.Entry{{Path: cmdDir, Type: "cache", Action: config.ActionCommand, Command: "true"}}}
	env := guard.Env{Home: home, Procs: testutil.NoProcs, Cwds: testutil.NoProcs}
	var out, errw bytes.Buffer
	if code := doCleanup(cfg, env, &opts{cleanup: true, yes: true, permanent: true}, time.Now(), &out, &errw); code != status.ExitNothing {
		t.Fatalf("text: code=%d out=%s err=%s", code, out.String(), errw.String())
	}
	if !strings.Contains(errw.String(), "nothing actionable") {
		t.Fatalf("text: stderr %q", errw.String())
	}
	out.Reset()
	items := plan.Build(cfg, env, nil, time.Now())
	if code := doCleanupJSON(cfg, env, &opts{cleanup: true, yes: true, permanent: true, jsonOut: true}, items, true, time.Now(), &out, &errw); code != status.ExitNothing {
		t.Fatalf("json: code=%d out=%s", code, out.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["error_kind"] != "nothing_actionable" {
		t.Fatalf("error_kind: %v %v", doc["error_kind"], err)
	}
	if code := doCleanup(cfg, env, &opts{cleanup: true}, time.Now(), &out, &errw); code != status.ExitOK {
		t.Fatalf("dry-run: code=%d", code)
	}

	cache := filepath.Join(home, "b", "cache")
	testutil.Write(t, filepath.Join(cache, "f"), 10)
	cfg.KnownDirs = append(cfg.KnownDirs, config.Entry{Path: cache, Type: "cache", Action: config.ActionRmContents})
	out.Reset()
	errw.Reset()
	if code := doCleanup(cfg, env, &opts{cleanup: true, yes: true, permanent: true}, time.Now(), &out, &errw); code != status.ExitOK {
		t.Fatalf("mixed text: code=%d out=%s err=%s", code, out.String(), errw.String())
	}
	testutil.Write(t, filepath.Join(cache, "f"), 10)
	out.Reset()
	items = plan.Build(cfg, env, nil, time.Now())
	if code := doCleanupJSON(cfg, env, &opts{cleanup: true, yes: true, permanent: true, jsonOut: true}, items, true, time.Now(), &out, &errw); code != status.ExitOK {
		t.Fatalf("mixed json: code=%d out=%s", code, out.String())
	}
}

// TestDefaultConfigRefusalsAloneNot7: the macOS default always has entries
// refused at plan time (commands, never entries, protected children); when
// its other entries run, the live cleanup is not "nothing actionable".
func TestDefaultConfigRefusalsAloneNot7(t *testing.T) {
	home := t.TempDir()
	cfg, err := config.Parse(config.DefaultFor("darwin"), home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Volume = home
	cfg.Policy.MinFreeGB, cfg.Policy.WarnFreeGB = 0, 0
	cfg.Policy.Quarantine = false
	for _, e := range cfg.KnownDirs {
		if strings.HasPrefix(e.Path, home) {
			testutil.Write(t, filepath.Join(e.Path, "f"), 10)
		}
	}
	env := guard.Env{Home: home, Procs: testutil.NoProcs, Cwds: testutil.NoProcs}
	items := plan.Build(cfg, env, nil, time.Now())
	refused, ok := 0, 0
	for _, it := range items {
		switch {
		case it.Action == config.ActionNever:
		case it.Refused != nil:
			refused++
		default:
			ok++
		}
	}
	if refused == 0 || ok == 0 {
		t.Fatalf("want refused and runnable work entries, got %d refused %d ok", refused, ok)
	}
	var out, errw bytes.Buffer
	if code := doCleanup(cfg, env, &opts{cleanup: true, yes: true, permanent: true}, time.Now(), &out, &errw); code != status.ExitOK {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errw.String())
	}
}

// heldBatch writes a quarantine batch with data and no manifest: held.
func heldBatch(t *testing.T, dir string, created time.Time) string {
	name := created.Format(plan.BatchLayout)
	testutil.Write(t, filepath.Join(dir, name, "stray"), 10)
	return name
}

// TestPurgeAllHeldExits7: a purge that removed nothing because every batch
// is held is 7 "nothing_actionable"; no batches is 0; purged beside held
// is 0 (held stays listed).
func TestPurgeAllHeldExits7(t *testing.T) {
	home := t.TempDir()
	p := quarantinePolicy(home)
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	now := time.Now()
	var out, errw bytes.Buffer
	if code := doPurge(cfg, &opts{purgeNow: true, yes: true}, now, &out, &errw); code != status.ExitOK {
		t.Fatalf("empty: code=%d err=%s", code, errw.String())
	}
	heldBatch(t, p.QuarantineDir, now.Add(-2*time.Hour))
	if code := doPurge(cfg, &opts{purgeNow: true, yes: true}, now, &out, &errw); code != status.ExitNothing {
		t.Fatalf("held text: code=%d out=%s err=%s", code, out.String(), errw.String())
	}
	out.Reset()
	if code := doPurge(cfg, &opts{purge: true, yes: true, jsonOut: true}, now, &out, &errw); code != status.ExitNothing {
		t.Fatalf("held json: code=%d out=%s", code, out.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["error_kind"] != "nothing_actionable" {
		t.Fatalf("error_kind: %v %v", doc["error_kind"], err)
	}
	// an empty scaffold batch (no manifest, no data) is purgeable
	name := now.Add(-time.Hour).Format(plan.BatchLayout)
	if err := os.MkdirAll(filepath.Join(p.QuarantineDir, name), 0o700); err != nil {
		t.Fatal(err)
	}
	if code := doPurge(cfg, &opts{purgeNow: true, yes: true}, now, &out, &errw); code != status.ExitOK {
		t.Fatalf("some+held: code=%d err=%s", code, errw.String())
	}
	if _, err := os.Stat(filepath.Join(p.QuarantineDir, name)); !os.IsNotExist(err) {
		t.Fatalf("scaffold batch not purged: %v", err)
	}
}

// TestPurgeUnsafeLockExits6: a symlinked store lock is refused, and a purge
// reports it as 6 "io" like a cleanup does, not 2 "refused".
func TestPurgeUnsafeLockExits6(t *testing.T) {
	home := t.TempDir()
	p := quarantinePolicy(home)
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	if err := os.MkdirAll(p.QuarantineDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "elsewhere")
	testutil.Write(t, target, 1)
	if err := os.Symlink(target, filepath.Join(p.QuarantineDir, ".oos-store.lock")); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := doPurge(cfg, &opts{purgeNow: true, yes: true}, time.Now(), &out, &errw); code != status.ExitIO {
		t.Fatalf("text: code=%d err=%s", code, errw.String())
	}
	out.Reset()
	if code := doPurge(cfg, &opts{purgeNow: true, yes: true, jsonOut: true}, time.Now(), &out, &errw); code != status.ExitIO {
		t.Fatalf("json: code=%d", code)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["error_kind"] != "io" {
		t.Fatalf("error_kind: %v %v", doc["error_kind"], err)
	}
}

func TestRestoreExitTable(t *testing.T) {
	cases := []struct {
		name              string
		restored, skipped int
		err               error
		code              int
		kind              string
	}{
		{"all restored", 3, 0, nil, status.ExitOK, ""},
		{"some skipped", 2, 1, nil, status.ExitPartial, "partial"},
		{"none restored", 0, 3, nil, status.ExitCritical, "refused"},
		{"error after some", 2, 0, errors.New("disk"), status.ExitPartial, "partial"},
		{"error before any", 0, 0, errors.New("disk"), status.ExitCritical, "refused"},
		{"busy", 0, 0, errBusy, status.ExitBusy, "busy"},
		{"unsafe store lock", 0, 0, fmt.Errorf("%w: hardlinked", plan.ErrStoreLock), status.ExitIO, "io"},
	}
	for _, c := range cases {
		code, kind := restoreExit(c.restored, c.skipped, c.err)
		if code != c.code || kind != c.kind {
			t.Errorf("%s: restoreExit = %d %q, want %d %q", c.name, code, kind, c.code, c.kind)
		}
	}
}

// TestCleanupNothingDoneExits2Refused: a live cleanup whose run is refused
// before anything is touched (here the per-run budget) is 2 "refused" in
// text and JSON, never 5.
func TestCleanupNothingDoneExits2Refused(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 1<<20)
	p := testutil.PolicyFor(home)
	p.MaxDeleteGBPerRun = 1e-9
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home,
		KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmContents}}}
	env := guard.Env{Home: home, Procs: testutil.NoProcs, Cwds: testutil.NoProcs}
	items := plan.Build(cfg, env, nil, time.Now())
	var out, errw bytes.Buffer
	if code := doCleanup(cfg, env, &opts{cleanup: true, yes: true, permanent: true}, time.Now(), &out, &errw); code != status.ExitCritical {
		t.Fatalf("text: code=%d out=%s err=%s", code, out.String(), errw.String())
	}
	out.Reset()
	errw.Reset()
	if code := doCleanupJSON(cfg, env, &opts{cleanup: true, yes: true, permanent: true, jsonOut: true}, items, true, time.Now(), &out, &errw); code != status.ExitCritical {
		t.Fatalf("json: code=%d out=%s err=%s", code, out.String(), errw.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["error_kind"] != "refused" {
		t.Fatalf("error_kind: %v %v", doc["error_kind"], err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Fatal("nothing may be removed")
	}
}

// TestCleanupBusyStoreLockExits4: a quarantine store locked by another oos
// process is retryable (4 "busy"), not an I/O failure.
func TestCleanupBusyStoreLockExits4(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	testutil.Write(t, filepath.Join(dir, "f"), 10)
	p := quarantinePolicy(home)
	if err := os.MkdirAll(p.QuarantineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(p.QuarantineDir, ".oos-store.lock"), os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home,
		KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmContents}}}
	env := guard.Env{Home: home, Procs: testutil.NoProcs, Cwds: testutil.NoProcs}
	var out, errw bytes.Buffer
	if code := doCleanup(cfg, env, &opts{cleanup: true, yes: true}, time.Now(), &out, &errw); code != status.ExitBusy {
		t.Fatalf("text: code=%d err=%s", code, errw.String())
	}
	out.Reset()
	items := plan.Build(cfg, env, nil, time.Now())
	if code := doCleanupJSON(cfg, env, &opts{cleanup: true, yes: true, jsonOut: true}, items, true, time.Now(), &out, &errw); code != status.ExitBusy {
		t.Fatalf("json: code=%d err=%s", code, errw.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["error_kind"] != "busy" {
		t.Fatalf("error_kind: %v %v", doc["error_kind"], err)
	}
}

// TestStuckTombstoneReported: a tombstone a purge could not finish is a
// held item with its reason in --verify-quarantine, -s and --purge, never
// an "invalid batch name" parse error.
func TestStuckTombstoneReported(t *testing.T) {
	home := t.TempDir()
	p := quarantinePolicy(home)
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	name := time.Now().Add(-48 * time.Hour).Format(plan.BatchLayout)
	tomb := filepath.Join(p.QuarantineDir, ".purging-"+name)
	testutil.Write(t, filepath.Join(tomb, "x", "f"), 10)

	var out, errw bytes.Buffer
	if code := doIntegrity(cfg, &opts{verifyQuarantine: true}, &out, &errw); code != status.ExitCritical {
		t.Fatalf("verify: code=%d out=%s err=%s", code, out.String(), errw.String())
	}
	if s := out.String(); strings.Contains(s, "invalid batch name") || !strings.Contains(s, ".purging-"+name) || !strings.Contains(s, "held: purge incomplete: ") {
		t.Fatalf("verify output:\n%s", s)
	}
	out.Reset()
	if code := doIntegrity(cfg, &opts{verifyQuarantine: true, jsonOut: true}, &out, &errw); code != status.ExitCritical {
		t.Fatalf("verify -j: code=%d", code)
	}
	var doc struct {
		Batches []struct {
			Held  string `json:"held"`
			Error string `json:"error"`
		} `json:"batches"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || len(doc.Batches) != 1 || !strings.HasPrefix(doc.Batches[0].Held, "purge incomplete: ") || doc.Batches[0].Error != "" {
		t.Fatalf("verify -j: %+v %v\n%s", doc, err, out.String())
	}

	out.Reset()
	doShow(cfg, "test", &opts{}, &out, &errw)
	if s := out.String(); !strings.Contains(s, "purge incomplete (retried by every purge): ") || strings.Contains(s, "no manifest") {
		t.Fatalf("show output:\n%s", s)
	}

	out.Reset()
	doPurge(cfg, &opts{purge: true}, time.Now(), &out, &errw)
	if s := out.String(); !strings.Contains(s, "tombstone could not be deleted: ") || !strings.Contains(s, "next purge finishes it") || strings.Contains(s, "--include-held") {
		t.Fatalf("purge output:\n%s", s)
	}
}

// TestUninspectedProcessesNote: a live run that could not inspect other
// users' processes says so once in text and in the cleanup JSON; the count
// is only known after execution, so a dry run never says it.
func TestUninspectedProcessesNote(t *testing.T) {
	old := otherUserProcesses
	otherUserProcesses = func() int { return 7 }
	defer func() { otherUserProcesses = old }()
	home := t.TempDir()
	dir := filepath.Join(home, "a", "cache")
	cfg := &config.Config{Version: 1, Volume: home, Policy: testutil.PolicyFor(home), Home: home,
		KnownDirs: []config.Entry{{Path: dir, Type: "cache", Action: config.ActionRmContents}}}
	env := guard.Env{Home: home, Procs: testutil.NoProcs, Cwds: testutil.NoProcs}
	live := &opts{cleanup: true, yes: true, permanent: true}
	var out, errw bytes.Buffer
	testutil.Write(t, filepath.Join(dir, "f"), 10)
	doCleanup(cfg, env, &opts{cleanup: true}, time.Now(), &out, &errw)
	if strings.Contains(out.String(), "processes of other users") {
		t.Fatalf("dry-run note:\n%s", out.String())
	}
	out.Reset()
	doCleanup(cfg, env, live, time.Now(), &out, &errw)
	if n := strings.Count(out.String(), "note: 7 processes of other users could not be inspected\n"); n != 1 {
		t.Fatalf("note lines %d:\n%s", n, out.String())
	}
	out.Reset()
	testutil.Write(t, filepath.Join(dir, "f"), 10)
	doCleanupJSON(cfg, env, &opts{cleanup: true, jsonOut: true}, plan.Build(cfg, env, nil, time.Now()), false, time.Now(), &out, &errw)
	if strings.Contains(out.String(), "other_user_processes_not_inspected") {
		t.Fatal("dry-run JSON must not carry the field")
	}
	out.Reset()
	doCleanupJSON(cfg, env, &opts{cleanup: true, yes: true, permanent: true, jsonOut: true}, plan.Build(cfg, env, nil, time.Now()), true, time.Now(), &out, &errw)
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["other_user_processes_not_inspected"] != float64(7) {
		t.Fatalf("json: %v %v", doc["other_user_processes_not_inspected"], err)
	}
	otherUserProcesses = func() int { return 0 }
	out.Reset()
	testutil.Write(t, filepath.Join(dir, "f"), 10)
	doCleanupJSON(cfg, env, &opts{cleanup: true, yes: true, permanent: true, jsonOut: true}, plan.Build(cfg, env, nil, time.Now()), true, time.Now(), &out, &errw)
	if strings.Contains(out.String(), "other_user_processes_not_inspected") {
		t.Fatal("field only when > 0")
	}
}

// TestShowLabelsInvalidManifest: -s says "manifest invalid: <reason>" for a
// batch whose manifest exists but fails to verify, and "no manifest" only
// when there is none.
func TestShowLabelsInvalidManifest(t *testing.T) {
	home := t.TempDir()
	p := quarantinePolicy(home)
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	now := time.Now()
	bad := heldBatch(t, p.QuarantineDir, now.Add(-2*time.Hour))
	if err := os.WriteFile(filepath.Join(p.QuarantineDir, bad, "manifest.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	heldBatch(t, p.QuarantineDir, now.Add(-time.Hour))
	var out, errw bytes.Buffer
	doShow(cfg, "test", &opts{}, &out, &errw)
	s := out.String()
	if !strings.Contains(s, bad) || !strings.Contains(s, "manifest invalid: ") || strings.Count(s, "no manifest, never auto-purged") != 1 {
		t.Fatalf("show output:\n%s", s)
	}
}
