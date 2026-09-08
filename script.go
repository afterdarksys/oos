package main

// Script-facing modes: the things a shell script or an agent wants to ask
// before it touches a disk. Every one of them is exit-code first and has a
// JSON form.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// applyOverrides lets a script tighten or loosen the thresholds for one run.
func applyOverrides(cfg *Config, o *opts) error {
	if o.critGB > 0 {
		cfg.Policy.MinFreeGB = o.critGB
	}
	if o.warnGB > 0 {
		cfg.Policy.WarnFreeGB = o.warnGB
	}
	if cfg.Policy.WarnFreeGB < cfg.Policy.MinFreeGB {
		return fmt.Errorf("--warn %.0f is below --critical %.0f", cfg.Policy.WarnFreeGB, cfg.Policy.MinFreeGB)
	}
	return nil
}

// doFree prints free space as a bare integer GB (floor) so shell can compare it.
func doFree(cfg *Config, o *opts, out, errw io.Writer) int {
	du, err := diskUsage(cfg.Volume)
	if err != nil {
		fmt.Fprintf(errw, "oos: statfs %s: %v\n", cfg.Volume, err)
		return exitUsage
	}
	label, code := status(cfg.Policy, du)
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"free_gb": du.FreeGB(), "total_gb": du.TotalGB(), "status": label})
		return code
	}
	fmt.Fprintf(out, "%d\n", int64(du.FreeGB()))
	return code
}

// whyResult is the answer to "what would oos do with this path".
type whyResult struct {
	Path     string   `json:"path"`
	Exists   bool     `json:"exists"`
	IsDir    bool     `json:"dir"`
	Bytes    int64    `json:"bytes"`
	Entry    *Entry   `json:"entry,omitempty"`   // the configured entry that covers it, if any
	Covered  string   `json:"covered,omitempty"` // exact | under-entry | contains-entry
	Verdict  string   `json:"verdict"`           // would-act | refused | unknown-allowed | unknown-refused | never
	Reasons  []string `json:"reasons"`
	ExitCode int      `json:"exit_code"`
}

// doWhy explains a path. Unknown paths are judged as if they were an
// rm-contents (dir) or rm (file) entry so the caller learns which guard
// would bite before writing config for it.
func doWhy(cfg *Config, env Env, o *opts, out, errw io.Writer) int {
	p := expandHome(o.why, env.Home)
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	r := whyResult{Path: p}
	if fi, err := os.Lstat(p); err == nil {
		r.Exists = true
		r.IsDir = fi.IsDir()
		if !o.quick {
			r.Bytes, _ = pathSize(p)
		}
	}
	var ent *Entry
	for _, e := range cfg.entries(nil) {
		e := e
		switch {
		case e.Path == p:
			ent, r.Covered = &e, "exact"
		case isUnder(p, e.Path) && ent == nil:
			ent, r.Covered = &e, "under-entry"
		case isUnder(e.Path, p) && ent == nil:
			ent, r.Covered = &e, "contains-entry"
		}
		if r.Covered == "exact" {
			break
		}
	}
	for _, nt := range cfg.Policy.NeverTouch {
		if isUnder(p, nt) {
			r.Reasons = append(r.Reasons, "under never_touch "+nt)
		}
	}
	judge := Entry{Path: p, Type: "adhoc", Action: ActionRmContents}
	if r.Exists && !r.IsDir {
		judge.Action = ActionRm
	}
	if ent != nil {
		r.Entry = ent
		if r.Covered == "exact" {
			judge = *ent
		} else {
			r.Reasons = append(r.Reasons, fmt.Sprintf("%s configured entry %s (%s)", r.Covered, ent.Path, ent.Action))
		}
	}
	switch {
	case ent != nil && r.Covered == "exact" && ent.Action == ActionNever:
		r.Verdict, r.ExitCode = "never", exitCritical
		r.Reasons = append(r.Reasons, "entry is marked never")
	case ent != nil && r.Covered == "under-entry" && ent.Action == ActionNever:
		r.Verdict, r.ExitCode = "never", exitCritical
		r.Reasons = append(r.Reasons, "inside an entry marked never; the parent protects it")
	case ent != nil && r.Covered == "exact" && ent.Action == ActionCommand:
		r.Verdict, r.ExitCode = "would-act", exitOK
		r.Reasons = append(r.Reasons, "via command "+ent.Command)
	default:
		if err := env.checkDeletable(cfg.Policy, judge); err != nil {
			r.Reasons = append(r.Reasons, err.Error())
			if ent != nil && r.Covered == "exact" {
				r.Verdict, r.ExitCode = "refused", exitCritical
			} else {
				r.Verdict, r.ExitCode = "unknown-refused", exitCritical
			}
		} else if ent != nil && r.Covered == "exact" {
			r.Verdict, r.ExitCode = "would-act", exitOK
			r.Reasons = append(r.Reasons, "all guards pass for "+ent.Action)
		} else {
			r.Verdict, r.ExitCode = "unknown-allowed", exitWarn
			r.Reasons = append(r.Reasons, "not in oos.json; guards would allow "+judge.Action+"; register it with --add before relying on that")
		}
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(r)
		return r.ExitCode
	}
	fmt.Fprintf(out, "%s\n", p)
	if r.Exists {
		fmt.Fprintf(out, "  %s, %s\n", map[bool]string{true: "directory", false: "file"}[r.IsDir], human(r.Bytes))
	} else {
		fmt.Fprintln(out, "  does not exist")
	}
	fmt.Fprintf(out, "  verdict: %s\n", r.Verdict)
	for _, reason := range r.Reasons {
		fmt.Fprintf(out, "  - %s\n", reason)
	}
	return r.ExitCode
}

// doEnsure makes at least o.ensure GB free, or says why it cannot. Order:
// expired quarantine batches first (no risk), then destructive entries
// largest first, then commands. Removals here are permanent, because the
// caller asked for space it can use right now; the audit log still records
// every path.
func doEnsure(cfg *Config, env Env, o *opts, now time.Time, out, errw io.Writer) int {
	target := o.ensure
	du, err := diskUsage(cfg.Volume)
	if err != nil {
		fmt.Fprintf(errw, "oos: statfs %s: %v\n", cfg.Volume, err)
		return exitUsage
	}
	live := o.yes && !o.no
	report := func(final DiskUsage, steps []string, reached bool) int {
		if o.jsonOut {
			_ = json.NewEncoder(out).Encode(map[string]any{
				"target_gb": target, "start_free_gb": du.FreeGB(), "free_gb": final.FreeGB(),
				"reached": reached, "live": live, "steps": steps,
			})
		} else {
			for _, s := range steps {
				fmt.Fprintln(out, "  "+s)
			}
			verb := "would reach"
			if live {
				verb = "reached"
			}
			if !reached {
				verb = "cannot reach"
				if live {
					verb = "did not reach"
				}
			}
			fmt.Fprintf(out, "ensure %.0f GB: %s; free %.1f GB -> %.1f GB\n", target, verb, du.FreeGB(), final.FreeGB())
		}
		if reached {
			return exitOK
		}
		return exitCritical
	}
	if du.FreeGB() >= target {
		return report(du, []string{fmt.Sprintf("already %.1f GB free", du.FreeGB())}, true)
	}
	need := int64((target - du.FreeGB()) * gb)
	var steps []string
	var projected int64

	// 1. expired quarantine
	if cfg.Policy.Quarantine {
		olderThan := time.Duration(cfg.Policy.QuarantineDays) * 24 * time.Hour
		bs, _ := listBatches(cfg.Policy.QuarantineDir)
		var expired int64
		for _, b := range bs {
			if b.Count >= 0 && now.Sub(b.Created) >= olderThan {
				expired += b.Bytes
			}
		}
		if expired > 0 {
			if live {
				freed, names, err := purgeBatches(cfg.Policy.QuarantineDir, olderThan, now, false)
				steps = append(steps, fmt.Sprintf("purged %d expired quarantine batches, %s (err=%v)", len(names), human(freed), err))
				projected += freed
			} else {
				steps = append(steps, fmt.Sprintf("purge %d expired quarantine batches, %s", len(bs), human(expired)))
				projected += expired
			}
		}
	}

	// 2. plan, largest destructive first, then commands
	items := buildPlan(cfg, env, splitTypes(o.types), now)
	var rm, cmds []PlanItem
	for _, it := range items {
		if it.Refused != nil {
			continue
		}
		if isDestructive(it.Action) && it.Deletable > 0 {
			rm = append(rm, it)
		} else if it.Action == ActionCommand {
			cmds = append(cmds, it)
		}
	}
	ordered := append(rm, cmds...)

	var logf *os.File
	if live {
		logf, err = openLog(cfg.Policy.LogFile)
		if err != nil {
			fmt.Fprintf(errw, "oos: cannot open log %s: %v; refusing to act without an audit log\n", cfg.Policy.LogFile, err)
			return exitCritical
		}
		defer logf.Close()
	}
	x := &Executor{Policy: cfg.Policy, Log: logf, Out: io.Discard, Now: time.Now, Run: shellRun, Move: os.Rename, Refs: env.references}
	var spent int64
	maxBytes := int64(cfg.Policy.MaxDeleteGBPerRun * gb)
	final := du
	for _, it := range ordered {
		if projected >= need {
			break
		}
		if isDestructive(it.Action) && spent+it.Deletable > maxBytes {
			steps = append(steps, fmt.Sprintf("skip %s: would exceed the %.0f GB per-run budget", it.Path, cfg.Policy.MaxDeleteGBPerRun))
			continue
		}
		if !live {
			steps = append(steps, fmt.Sprintf("%s %s (%s)", it.Action, it.Path, human(it.Deletable)))
			projected += it.Deletable
			spent += it.Deletable
			continue
		}
		before, _ := diskUsage(cfg.Volume)
		if _, err := x.Execute([]PlanItem{it}); err != nil {
			steps = append(steps, fmt.Sprintf("%s %s: %v", it.Action, it.Path, err))
			continue
		}
		after, _ := diskUsage(cfg.Volume)
		got := int64(after.Free) - int64(before.Free)
		spent += it.Deletable
		projected += got
		final = after
		steps = append(steps, fmt.Sprintf("%s %s: %s freed", it.Action, it.Path, human(got)))
	}
	if live {
		final, _ = diskUsage(cfg.Volume)
		if st, err := loadState(cfg.Policy.StateFile); err == nil {
			st.record("ensure", final, now)
			_ = saveState(cfg.Policy.StateFile, st)
		}
		return report(final, steps, final.FreeGB() >= target)
	}
	proj := DiskUsage{Free: du.Free + uint64(projected), Total: du.Total}
	if projected < need {
		steps = append(steps, fmt.Sprintf("short by %s even after every allowed action", human(need-projected)))
	}
	return report(proj, steps, projected >= need)
}

// configFileForEdit returns the config path --add/--forget should modify,
// seeding ~/.config/oos/oos.json from the embedded default when nothing
// on disk exists yet.
func configFileForEdit(o *opts, home string) (string, error) {
	if o.config != "" {
		return o.config, nil
	}
	for _, c := range configCandidates(home) {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	dst := filepath.Join(home, ".config", "oos", "oos.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, defaultConfig, 0o644); err != nil {
		return "", err
	}
	return dst, nil
}

// rewriteConfig applies edit to the raw JSON document, validates the result
// by parsing it exactly as loadConfig would, and only then writes it.
func rewriteConfig(path, home string, edit func(doc map[string]any) error) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if err := edit(doc); err != nil {
		return err
	}
	outB, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if _, err := parseConfig(outB, home); err != nil {
		return fmt.Errorf("refusing to write: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(outB, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func doAdd(env Env, o *opts, out, errw io.Writer) int {
	path, err := configFileForEdit(o, env.Home)
	if err != nil {
		fmt.Fprintln(errw, "oos:", err)
		return exitUsage
	}
	target := expandHome(o.add, env.Home)
	if !filepath.IsAbs(target) {
		if abs, err := filepath.Abs(target); err == nil {
			target = abs
		}
	}
	// Store with ~ when under home so the config stays portable.
	stored := target
	if isUnder(target, env.Home) {
		stored = "~" + strings.TrimPrefix(target, env.Home)
	}
	fi, statErr := os.Lstat(target)
	list := "known_dirs"
	if statErr == nil && !fi.IsDir() {
		list = "known_files"
	}
	entry := map[string]any{"path": stored, "type": o.addType, "action": o.addAction}
	if o.addCommand != "" {
		entry["command"] = o.addCommand
	}
	if o.addNote != "" {
		entry["note"] = o.addNote
	}
	if o.addStale > 0 {
		entry["stale_after_hours"] = o.addStale
	}
	if o.addUseCase != "" {
		entry["use_case"] = o.addUseCase
	}
	err = rewriteConfig(path, env.Home, func(doc map[string]any) error {
		arr, _ := doc[list].([]any)
		for _, x := range arr {
			if m, ok := x.(map[string]any); ok && (m["path"] == stored || m["path"] == target) {
				return fmt.Errorf("%s already lists %s; --forget it first", list, stored)
			}
		}
		doc[list] = append(arr, entry)
		return nil
	})
	if err != nil {
		fmt.Fprintln(errw, "oos:", err)
		return exitUsage
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"config": path, "list": list, "entry": entry})
	} else {
		fmt.Fprintf(out, "added %s to %s in %s (%s, %s)\n", stored, list, path, o.addType, o.addAction)
	}
	return exitOK
}

func doForget(env Env, o *opts, out, errw io.Writer) int {
	path, err := configFileForEdit(o, env.Home)
	if err != nil {
		fmt.Fprintln(errw, "oos:", err)
		return exitUsage
	}
	target := expandHome(o.forget, env.Home)
	removed := ""
	err = rewriteConfig(path, env.Home, func(doc map[string]any) error {
		for _, list := range []string{"known_dirs", "known_files"} {
			arr, _ := doc[list].([]any)
			var keep []any
			for _, x := range arr {
				m, ok := x.(map[string]any)
				if ok {
					if ps, _ := m["path"].(string); expandHome(ps, env.Home) == target {
						removed = list
						continue
					}
				}
				keep = append(keep, x)
			}
			if keep == nil {
				keep = []any{}
			}
			doc[list] = keep
		}
		if removed == "" {
			return errors.New("no entry matches " + target)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(errw, "oos:", err)
		return exitUsage
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"config": path, "list": removed, "path": target})
	} else {
		fmt.Fprintf(out, "removed %s from %s in %s\n", target, removed, path)
	}
	return exitOK
}

// doLogTail prints the last n audit log lines so a script can confirm what
// a previous oos run actually did.
func doLogTail(cfg *Config, o *opts, out, errw io.Writer) int {
	f, err := os.Open(cfg.Policy.LogFile)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(errw, "oos: no audit log yet at %s\n", cfg.Policy.LogFile)
			return exitOK
		}
		fmt.Fprintln(errw, "oos:", err)
		return exitUsage
	}
	defer f.Close()
	ring := make([]string, 0, o.logTail)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if len(ring) == o.logTail {
			ring = ring[1:]
		}
		ring = append(ring, sc.Text())
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(ring)
		return exitOK
	}
	for _, l := range ring {
		fmt.Fprintln(out, l)
	}
	return exitOK
}
