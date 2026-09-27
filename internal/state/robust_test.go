package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestUpdateLockHasDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if err := unix.Flock(int(lf.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	old := LockWait
	LockWait = 200 * time.Millisecond
	defer func() { LockWait = old }()
	start := time.Now()
	_, err = Update(path, func(*State) {})
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("a held lock must time out, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("waited far past the deadline")
	}
	unix.Flock(int(lf.Fd()), unix.LOCK_UN)
	if _, err := Update(path, func(*State) {}); err != nil {
		t.Fatalf("released lock: %v", err)
	}
}

func TestOpenLogRotatesAndKeepsEveryLine(t *testing.T) {
	oldB, oldK := LogRotateBytes, LogKeep
	LogRotateBytes, LogKeep = 100, 3
	defer func() { LogRotateBytes, LogKeep = oldB, oldK }()
	path := filepath.Join(t.TempDir(), "oos.log")
	for i := 0; i < 40; i++ {
		f, err := OpenLog(path)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(f, "line %02d xxxxxxxxxxxxxxxxxx\n", i)
		f.Close()
	}
	for _, s := range []string{"", ".1", ".2", ".3"} {
		if _, err := os.Stat(path + s); err != nil {
			t.Errorf("expected %s%s: %v", path, s, err)
		}
	}
	if _, err := os.Stat(path + ".4"); err == nil {
		t.Error("only LogKeep generations are kept")
	}
	// the newest lines are all present across the live file and the last generation
	var all string
	for _, s := range []string{".3", ".2", ".1", ""} {
		b, _ := os.ReadFile(path + s)
		all += string(b)
	}
	if !strings.HasSuffix(all, "line 39 xxxxxxxxxxxxxxxxxx\n") || !strings.Contains(all, "line 30 ") {
		t.Errorf("recent lines lost:\n%s", all)
	}
}

func TestRotatingLogRotatesWhileOpen(t *testing.T) {
	oldB := LogRotateBytes
	LogRotateBytes = 64
	defer func() { LogRotateBytes = oldB }()
	path := filepath.Join(t.TempDir(), "daemon.log")
	l, err := OpenRotatingLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 10; i++ {
		fmt.Fprintf(l, "tick %d ..........................\n", i)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal("a long-lived writer must rotate too")
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "tick 9") {
		t.Errorf("the latest line belongs in the live file: %q", b)
	}
}

func TestSweepLeftovers(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "bigfile.json")
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	mk := func(name string, mt time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, mt, mt)
		return p
	}
	oldTemps := []string{mk(".oos-record-123", old), mk(".bigfile.json.tmp-9", old), mk(".quarantine-index-4", old), mk(".oos-reserve.tmp", old)}
	fresh := mk(".oos-record-new", now)
	reserve := mk(".oos-reserve", old) // the reserve itself is never a leftover
	foreign := mk(".other.tmp-1", old) // not a name oos writes here
	user := mk("notes.txt", old)
	var corrupt []string
	for i := 0; i < 5; i++ {
		corrupt = append(corrupt, mk(fmt.Sprintf("bigfile.json.corrupt-2026010%d-000000", i), now.Add(time.Duration(i)*time.Minute)))
	}
	os.Symlink("/etc/hosts", filepath.Join(dir, ".oos-record-link"))
	got := SweepLeftovers([]string{stateFile, ""}, now)
	for _, p := range append(oldTemps, corrupt[0], corrupt[1]) {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s should be swept", p)
		}
	}
	for _, p := range append([]string{fresh, foreign, user, reserve, filepath.Join(dir, ".oos-record-link")}, corrupt[2:]...) {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s must stay: %v", p, err)
		}
	}
	if len(got) != 6 {
		t.Errorf("removed %v", got)
	}
}
