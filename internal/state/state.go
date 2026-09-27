package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"

	"github.com/afterdarksys/oos/internal/audit"
	"github.com/afterdarksys/oos/internal/size"
)

// historyCap keeps about three and a half days of five-minute daemon ticks.
const historyCap = 1000

// State is bigfile.json: what oos last saw. It is a cache of observations,
// never an input to any decision about deleting.
type State struct {
	UpdatedAt time.Time        `json:"updated_at"`
	Volume    string           `json:"volume"`
	FreeGB    float64          `json:"free_gb"`
	TotalGB   float64          `json:"total_gb"`
	Known     map[string]int64 `json:"known_bytes"`
	BigFiles  []size.BigFile   `json:"big_files"`
	ScanRoot  string           `json:"scan_root,omitempty"`
	ScannedAt time.Time        `json:"scanned_at,omitempty"`
	Audit     []audit.Row      `json:"audit,omitempty"`
	AuditRoot string           `json:"audit_root,omitempty"`
	AuditedAt time.Time        `json:"audited_at,omitempty"`
	History   []HistoryPoint   `json:"history"`

	// loadErr is set when the file exists but could not be read; Save refuses
	// such a state so an empty stand-in never overwrites the real history.
	loadErr error
}

type HistoryPoint struct {
	At     time.Time `json:"at"`
	FreeGB float64   `json:"free_gb"`
	Event  string    `json:"event"`
}

// Load reads the state file. It always returns a usable *State: on a read
// error other than not-exist the state is empty, the error is returned too,
// and Save refuses to write that empty state back over the unreadable file.
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{Known: map[string]int64{}}, nil
	}
	if err != nil {
		return &State{Known: map[string]int64{}, loadErr: err}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		// A corrupt state file must not block the tool; start fresh but keep the old bytes.
		_ = os.Rename(path, path+".corrupt-"+time.Now().Format("20060102-150405"))
		return &State{Known: map[string]int64{}}, nil
	}
	if s.Known == nil {
		s.Known = map[string]int64{}
	}
	return &s, nil
}

// Save writes atomically: a unique temp file, fsync, rename, fsync the
// directory. A state whose Load failed is never written.
func Save(path string, s *State) error {
	if s.loadErr != nil {
		return fmt.Errorf("state %s was unreadable, not overwriting it: %w", path, s.loadErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	sort.SliceStable(s.History, func(i, j int) bool { return s.History[i].At.Before(s.History[j].At) })
	if len(s.History) > historyCap {
		s.History = s.History[len(s.History)-historyCap:]
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}

func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Update is load-modify-save under an exclusive flock on path+".lock", so the
// daemon, the agent and the CLI do not lose each other's writes. When the
// file cannot be read, fn is not applied, nothing is written, and the
// returned state is an empty stand-in alongside the error.
func Update(path string, fn func(*State)) (*State, error) {
	if path == "" {
		return &State{Known: map[string]int64{}}, errors.New("no state file configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return &State{Known: map[string]int64{}}, err
	}
	lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return &State{Known: map[string]int64{}}, err
	}
	defer lf.Close()
	if err := lockWithDeadline(lf, LockWait); err != nil {
		return &State{Known: map[string]int64{}}, fmt.Errorf("state lock %s: %w", path+".lock", err)
	}
	defer unix.Flock(int(lf.Fd()), unix.LOCK_UN)
	s, err := Load(path)
	if err != nil {
		return s, err
	}
	fn(s)
	return s, Save(path, s)
}

// LockWait bounds how long Update waits for the state lock. A holder that
// was SIGSTOPped (or hung on a dead mount) must not stall the daemon forever.
var LockWait = 10 * time.Second

// ErrLockTimeout is returned when the state lock stays held past LockWait.
var ErrLockTimeout = errors.New("timed out waiting for the lock; another oos process holds it")

// lockWithDeadline polls a non-blocking exclusive flock until it succeeds or
// wait elapses.
func lockWithDeadline(f *os.File, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	backoff := time.Millisecond
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return err
		}
		if !time.Now().Before(deadline) {
			return ErrLockTimeout
		}
		time.Sleep(backoff)
		if backoff < 50*time.Millisecond {
			backoff *= 2
		}
	}
}

func (s *State) Record(event string, du size.DiskUsage, now time.Time) {
	s.UpdatedAt = now
	s.FreeGB = du.FreeGB()
	s.TotalGB = du.TotalGB()
	s.History = append(s.History, HistoryPoint{At: now, FreeGB: du.FreeGB(), Event: event})
}

// OpenLog opens an append-only log, rotating it first when it has reached
// LogRotateBytes. See rotate for the crash-safety argument.
func OpenLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil && LogRotateBytes > 0 && fi.Size() >= LogRotateBytes {
		// a failed rotation never blocks the audit line: append to the big file
		_ = rotate(path)
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}
