// oos: out of space. Tracks known large directories and files, reports disk
// headroom against a policy, and cleans up only what the config says is safe,
// only with --yes, only inside the per-run budget, and only after every guard
// passes. Anything it cannot prove safe, it refuses.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	exitOK       = 0
	exitWarn     = 1
	exitCritical = 2
	exitUsage    = 3
)

type opts struct {
	check, known, cleanup, show, quick, yes, no, jsonOut, verbose, initCfg bool
	scan, types, config                                                    string
	minMB                                                                  int64
}

func parseFlags(args []string, stderr io.Writer) (*opts, error) {
	o := &opts{}
	fs := flag.NewFlagSet("oos", flag.ContinueOnError)
	fs.SetOutput(stderr)
	both := func(long, short string, set func(*flag.FlagSet, string)) {
		set(fs, long)
		if short != "" {
			set(fs, short)
		}
	}
	b := func(dst *bool, usage string) func(*flag.FlagSet, string) {
		return func(fs *flag.FlagSet, n string) { fs.BoolVar(dst, n, false, usage) }
	}
	s := func(dst *string, usage string) func(*flag.FlagSet, string) {
		return func(fs *flag.FlagSet, n string) { fs.StringVar(dst, n, "", usage) }
	}
	both("check", "c", b(&o.check, "report free space vs policy and size every known entry"))
	both("known", "k", b(&o.known, "list known large dirs and files with current sizes"))
	both("cleanup", "C", b(&o.cleanup, "plan cleanup of known entries; dry-run unless --yes"))
	both("show", "s", b(&o.show, "print the resolved config, policy and state"))
	both("scan", "S", s(&o.scan, "scan DIR for big files and record them in the state file"))
	both("types", "t", s(&o.types, "comma-separated entry types to include (cache,build,vm,...)"))
	both("config", "f", s(&o.config, "config file path (default: ./oos.json, ~/.config/oos/oos.json, embedded)"))
	both("yes", "y", b(&o.yes, "actually delete; without it --cleanup only prints the plan"))
	both("no", "n", b(&o.no, "force dry-run even if --yes is present"))
	both("quick", "q", b(&o.quick, "skip sizing known entries (fast --check)"))
	both("json", "j", b(&o.jsonOut, "machine-readable output"))
	both("verbose", "v", b(&o.verbose, "show refusal reasons for every entry"))
	both("init", "i", b(&o.initCfg, "write the default config to ~/.config/oos/oos.json"))
	fs.Int64Var(&o.minMB, "min-mb", 0, "minimum file size for --scan (default: policy.big_file_min_mb)")
	fs.Int64Var(&o.minMB, "m", 0, "alias for --min-mb")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: oos [-c|--check] [-k|--known] [-C|--cleanup] [-s|--show] [-S|--scan DIR]")
		fmt.Fprintln(stderr, "           [-t|--types LIST] [-f|--config FILE] [-y|--yes] [-n|--no] [-q|--quick]")
		fmt.Fprintln(stderr, "           [-j|--json] [-v|--verbose] [-i|--init] [-m|--min-mb N]")
		fmt.Fprintln(stderr)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	modes := 0
	for _, m := range []bool{o.check, o.known, o.cleanup, o.show, o.scan != "", o.initCfg} {
		if m {
			modes++
		}
	}
	if modes == 0 {
		o.check = true
		o.quick = true
	}
	return o, nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	o, err := parseFlags(args, stderr)
	if err != nil {
		return exitUsage
	}
	env, err := realEnv()
	if err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return exitUsage
	}
	if o.initCfg {
		return doInit(env, stdout, stderr)
	}
	cfg, src, err := loadConfig(o.config, env.Home)
	if err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return exitUsage
	}
	now := time.Now()
	code := exitOK
	worst := func(c int) {
		if c > code {
			code = c
		}
	}
	if o.show {
		worst(doShow(cfg, src, o, stdout))
	}
	if o.check || o.known {
		worst(doCheck(cfg, env, o, now, stdout, stderr))
	}
	if o.scan != "" {
		worst(doScan(cfg, o, now, stdout, stderr))
	}
	if o.cleanup {
		worst(doCleanup(cfg, env, o, now, stdout, stderr))
	}
	return code
}

func doInit(env Env, stdout, stderr io.Writer) int {
	dst := filepath.Join(env.Home, ".config", "oos", "oos.json")
	if _, err := os.Stat(dst); err == nil {
		fmt.Fprintf(stderr, "oos: %s already exists; not overwriting\n", dst)
		return exitUsage
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return exitUsage
	}
	if err := os.WriteFile(dst, defaultConfig, 0o644); err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "wrote %s\n", dst)
	return exitOK
}

func doShow(cfg *Config, src string, o *opts, out io.Writer) int {
	st, _ := loadState(cfg.Policy.StateFile)
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"config_source": src, "config": cfg, "state": st})
		return exitOK
	}
	fmt.Fprintf(out, "config: %s\n", src)
	fmt.Fprintf(out, "volume: %s\n", cfg.Volume)
	p := cfg.Policy
	fmt.Fprintf(out, "policy:\n")
	fmt.Fprintf(out, "  free thresholds     warn < %.0f GB, critical < %.0f GB\n", p.WarnFreeGB, p.MinFreeGB)
	fmt.Fprintf(out, "  require --yes       %v\n", p.RequireYes)
	fmt.Fprintf(out, "  max delete per run  %.0f GB\n", p.MaxDeleteGBPerRun)
	fmt.Fprintf(out, "  allow outside home  %v\n", p.AllowOutsideHome)
	fmt.Fprintf(out, "  allow commands      %v\n", p.AllowCommands)
	fmt.Fprintf(out, "  min path depth      %d\n", p.MinPathDepth)
	fmt.Fprintf(out, "  log file            %s\n", p.LogFile)
	fmt.Fprintf(out, "  state file          %s\n", p.StateFile)
	fmt.Fprintf(out, "  never touch:\n")
	for _, nt := range p.NeverTouch {
		fmt.Fprintf(out, "    %s\n", nt)
	}
	fmt.Fprintf(out, "known dirs (%d):\n", len(cfg.KnownDirs))
	for _, e := range cfg.KnownDirs {
		printEntry(out, e)
	}
	fmt.Fprintf(out, "known files (%d):\n", len(cfg.KnownFiles))
	for _, e := range cfg.KnownFiles {
		printEntry(out, e)
	}
	if st != nil && !st.UpdatedAt.IsZero() {
		fmt.Fprintf(out, "state: updated %s, free %.1f GB, %d sized entries, %d big files, %d history points\n",
			st.UpdatedAt.Local().Format("2006-01-02 15:04"), st.FreeGB, len(st.Known), len(st.BigFiles), len(st.History))
	} else {
		fmt.Fprintf(out, "state: none yet (%s)\n", p.StateFile)
	}
	return exitOK
}

func printEntry(out io.Writer, e Entry) {
	extra := ""
	if e.Command != "" {
		extra = fmt.Sprintf("  cmd=%q", e.Command)
	}
	if len(e.GuardProcesses) > 0 {
		extra += fmt.Sprintf("  guard=%s", strings.Join(e.GuardProcesses, ","))
	}
	fmt.Fprintf(out, "  %-8s %-12s %s%s\n", e.Type, e.Action, e.Path, extra)
	if e.Note != "" {
		fmt.Fprintf(out, "           %s\n", e.Note)
	}
}

func status(p Policy, du DiskUsage) (string, int) {
	switch {
	case du.FreeGB() < p.MinFreeGB:
		return "CRITICAL", exitCritical
	case du.FreeGB() < p.WarnFreeGB:
		return "WARN", exitWarn
	}
	return "OK", exitOK
}

func doCheck(cfg *Config, env Env, o *opts, now time.Time, out, errw io.Writer) int {
	du, err := diskUsage(cfg.Volume)
	if err != nil {
		fmt.Fprintf(errw, "oos: statfs %s: %v\n", cfg.Volume, err)
		return exitUsage
	}
	label, code := status(cfg.Policy, du)
	var items []PlanItem
	if !o.quick {
		items = buildPlan(cfg, env, splitTypes(o.types))
	}
	st, _ := loadState(cfg.Policy.StateFile)
	st.Volume = cfg.Volume
	for _, it := range items {
		if it.Refused == nil || it.Bytes > 0 {
			st.Known[it.Path] = it.Bytes
		}
	}
	st.record("check", du, now)
	if err := saveState(cfg.Policy.StateFile, st); err != nil {
		fmt.Fprintf(errw, "oos: save state: %v\n", err)
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{
			"volume": cfg.Volume, "free_gb": du.FreeGB(), "total_gb": du.TotalGB(),
			"status": label, "known": planJSON(items),
		})
		return code
	}
	fmt.Fprintf(out, "oos %s  %s\n", strings.ToLower(label), cfg.Volume)
	fmt.Fprintf(out, "  free %.1f GB of %.1f GB (%.1f%%)   warn < %.0f GB   critical < %.0f GB\n",
		du.FreeGB(), du.TotalGB(), du.FreePct(), cfg.Policy.WarnFreeGB, cfg.Policy.MinFreeGB)
	if o.quick {
		fmt.Fprintln(out, "  (quick: known entries not sized; drop --quick or use --known)")
		return code
	}
	fmt.Fprintf(out, "known entries (%d, largest first):\n", len(items))
	var reclaimable int64
	for _, it := range items {
		mark := " "
		if it.Refused == nil && it.Action != ActionCommand {
			mark = "*"
			reclaimable += it.Bytes
		} else if it.Refused == nil {
			mark = "c"
		}
		fmt.Fprintf(out, "  %s %9s  %-8s %-12s %s\n", mark, human(it.Bytes), it.Type, it.Action, it.Path)
		if o.verbose && it.Refused != nil {
			fmt.Fprintf(out, "                refused: %v\n", it.Refused)
		}
	}
	fmt.Fprintf(out, "  * reclaimable by --cleanup --yes now: %s   c = via command\n", human(reclaimable))
	return code
}

func planJSON(items []PlanItem) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m := map[string]any{"path": it.Path, "type": it.Type, "action": it.Action, "bytes": it.Bytes}
		if it.Refused != nil {
			m["refused"] = it.Refused.Error()
		}
		out = append(out, m)
	}
	return out
}

func doScan(cfg *Config, o *opts, now time.Time, out, errw io.Writer) int {
	root := filepath.Clean(o.scan)
	minMB := o.minMB
	if minMB <= 0 {
		minMB = cfg.Policy.BigFileMinMB
	}
	hits, err := scanBig(root, minMB*1024*1024, cfg.Policy.ScanTopN)
	if err != nil {
		fmt.Fprintf(errw, "oos: scan %s: %v\n", root, err)
		return exitUsage
	}
	st, _ := loadState(cfg.Policy.StateFile)
	st.BigFiles = hits
	st.ScanRoot = root
	st.ScannedAt = now
	if du, err := diskUsage(cfg.Volume); err == nil {
		st.record("scan", du, now)
	}
	if err := saveState(cfg.Policy.StateFile, st); err != nil {
		fmt.Fprintf(errw, "oos: save state: %v\n", err)
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(hits)
		return exitOK
	}
	fmt.Fprintf(out, "big files under %s (>= %d MB, top %d):\n", root, minMB, cfg.Policy.ScanTopN)
	for _, h := range hits {
		fmt.Fprintf(out, "  %9s  %s  %s\n", human(h.Bytes), h.ModTime.Format("2006-01-02"), h.Path)
	}
	fmt.Fprintf(out, "recorded %d files in %s\n", len(hits), cfg.Policy.StateFile)
	return exitOK
}

func doCleanup(cfg *Config, env Env, o *opts, now time.Time, out, errw io.Writer) int {
	live := o.yes && !o.no
	if !cfg.Policy.RequireYes {
		live = !o.no
	}
	items := buildPlan(cfg, env, splitTypes(o.types))
	// Sizes are expensive; keep them even on a dry-run so --show has something to say.
	if st, err := loadState(cfg.Policy.StateFile); err == nil {
		st.Volume = cfg.Volume
		for _, it := range items {
			if it.Bytes > 0 {
				st.Known[it.Path] = it.Bytes
			}
		}
		if du, err := diskUsage(cfg.Volume); err == nil {
			st.record("plan", du, now)
		}
		_ = saveState(cfg.Policy.StateFile, st)
	}
	var planned int64
	fmt.Fprintf(out, "cleanup plan (%s):\n", map[bool]string{true: "LIVE", false: "dry-run"}[live])
	for _, it := range items {
		if it.Refused != nil {
			fmt.Fprintf(out, "  skip   %9s  %s\n", human(it.Bytes), it.Path)
			if o.verbose || it.Action != ActionNever {
				fmt.Fprintf(out, "         %v\n", it.Refused)
			}
			continue
		}
		switch it.Action {
		case ActionCommand:
			fmt.Fprintf(out, "  run    %9s  %s  -> %q\n", human(it.Bytes), it.Path, it.Command)
		default:
			planned += it.Bytes
			fmt.Fprintf(out, "  delete %9s  %s  (%s)\n", human(it.Bytes), it.Path, it.Action)
		}
	}
	fmt.Fprintf(out, "  planned deletions: %s   budget: %.0f GB\n", human(planned), cfg.Policy.MaxDeleteGBPerRun)
	if !live {
		fmt.Fprintln(out, "dry-run: nothing touched. Add --yes to execute.")
		return exitOK
	}
	logf, err := openLog(cfg.Policy.LogFile)
	if err != nil {
		fmt.Fprintf(errw, "oos: cannot open log %s: %v; refusing to delete without an audit log\n", cfg.Policy.LogFile, err)
		return exitCritical
	}
	defer logf.Close()
	before, _ := diskUsage(cfg.Volume)
	x := &Executor{Policy: cfg.Policy, Log: logf, Out: out, Now: time.Now, Run: shellRun}
	fmt.Fprintln(out, "executing:")
	freed, err := x.Execute(items)
	if err != nil {
		fmt.Fprintf(errw, "oos: refused: %v\n", err)
		return exitCritical
	}
	after, _ := diskUsage(cfg.Volume)
	st, _ := loadState(cfg.Policy.StateFile)
	st.record("cleanup", after, now)
	_ = saveState(cfg.Policy.StateFile, st)
	fmt.Fprintf(out, "done: %s removed by rm actions; volume free %.1f GB -> %.1f GB\n",
		human(freed), before.FreeGB(), after.FreeGB())
	_, code := status(cfg.Policy, after)
	return code
}

func splitTypes(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, ",")
}
