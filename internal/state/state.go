package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/afterdarksys/oos/internal/audit"
	"github.com/afterdarksys/oos/internal/size"
)

const historyCap = 200

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
}

type HistoryPoint struct {
	At     time.Time `json:"at"`
	FreeGB float64   `json:"free_gb"`
	Event  string    `json:"event"`
}

func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{Known: map[string]int64{}}, nil
	}
	if err != nil {
		return nil, err
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

// Save writes atomically: temp file then rename.
func Save(path string, s *State) error {
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *State) Record(event string, du size.DiskUsage, now time.Time) {
	s.UpdatedAt = now
	s.FreeGB = du.FreeGB()
	s.TotalGB = du.TotalGB()
	s.History = append(s.History, HistoryPoint{At: now, FreeGB: du.FreeGB(), Event: event})
}

func OpenLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}
