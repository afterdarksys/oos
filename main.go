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
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const version = "0.2.0"

const (
	exitOK       = 0
	exitWarn     = 1
	exitCritical = 2
	exitUsage    = 3
)

type opts struct {
	check, known, cleanup, show, diff, quick, yes, no, jsonOut, verbose bool
	initCfg, notify, installAgent, uninstallAgent, purge, purgeNow, ver bool
	scan, types, config, restore, audit                                 string
	minMB                                                               int64

	// script-facing
	quiet, free, auditHome               bool
	ensure, warnGB, critGB               float64
	why, add, forget, addType, addAction string
	addCommand, addNote                  string
	addStale, logTail                    int
}

type cmdRunner func(name string, args ...string) error

func execRun(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// notifyFn and agentRun are indirections so tests can observe without side effects.
var (
	notifyFn = notify
	agentRun = cmdRunner(execRun)
)

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
	both("show", "s", b(&o.show, "print the resolved config, policy, state and quarantine batches"))
	both("diff", "d", b(&o.diff, "size known entries and report growth since the last recorded sizes"))
	both("scan", "S", s(&o.scan, "scan DIR for big files and record them in the state file"))
	fs.StringVar(&o.audit, "audit", "", "audit DIR one level deep: size, age and known/unknown status of every entry; -v shows all hints")
	fs.BoolVar(&o.auditHome, "A", false, "audit the home directory (same as --audit ~)")
	both("types", "t", s(&o.types, "comma-separated entry types to include (cache,build,vm,...)"))
	both("config", "f", s(&o.config, "config file path (default: ./oos.json, ~/.config/oos/oos.json, embedded)"))
	both("yes", "y", b(&o.yes, "actually act; without it --cleanup and --purge only print the plan"))
	both("no", "n", b(&o.no, "force dry-run even if --yes is present"))
	both("quick", "q", b(&o.quick, "skip sizing known entries (fast --check)"))
	both("json", "j", b(&o.jsonOut, "machine-readable output"))
	both("verbose", "v", b(&o.verbose, "show refusal reasons and kept children"))
	both("init", "i", b(&o.initCfg, "write the platform default config to ~/.config/oos/oos.json"))
	both("notify", "N", b(&o.notify, "with --check: send a desktop notification when status is not OK"))
	both("version", "V", b(&o.ver, "print version"))
	fs.BoolVar(&o.installAgent, "install-agent", false, "install an hourly --check --quick --notify job (launchd or systemd user timer)")
	fs.BoolVar(&o.uninstallAgent, "uninstall-agent", false, "remove the hourly job")
	fs.BoolVar(&o.purge, "purge", false, "permanently delete quarantine batches older than policy.quarantine_days (needs --yes)")
	fs.BoolVar(&o.purgeNow, "purge-now", false, "permanently delete every quarantine batch (needs --yes)")
	fs.StringVar(&o.restore, "restore", "", "move every path in quarantine BATCH back where it came from")
	fs.Int64Var(&o.minMB, "min-mb", 0, "minimum file size for --scan (default: policy.big_file_min_mb)")
	fs.Int64Var(&o.minMB, "m", 0, "alias for --min-mb")
	both("quiet", "Q", b(&o.quiet, "no stdout; exit code only"))
	both("free", "F", b(&o.free, "print free space as an integer GB and exit with the status code"))
	both("ensure", "E", func(fs *flag.FlagSet, n string) {
		fs.Float64Var(&o.ensure, n, 0, "make at least GB free: expired quarantine first, then entries largest first; permanent removals; needs --yes")
	})
	both("why", "W", s(&o.why, "explain what oos would do with PATH and which guard would stop it (exit 0 act, 1 unknown-allowed, 2 refused)"))
	fs.Float64Var(&o.warnGB, "warn", 0, "override policy.warn_free_gb for this run")
	fs.Float64Var(&o.critGB, "critical", 0, "override policy.min_free_gb for this run")
	fs.StringVar(&o.add, "add", "", "add PATH to the config file (with --type, --action, optional --command --note --stale-hours)")
	fs.StringVar(&o.addType, "type", "", "entry type for --add")
	fs.StringVar(&o.addAction, "action", "", "entry action for --add: never|rm-contents|rm|rm-stale-children|command")
	fs.StringVar(&o.addCommand, "command", "", "command for --add --action command")
	fs.StringVar(&o.addNote, "note", "", "note for --add")
	fs.IntVar(&o.addStale, "stale-hours", 0, "stale_after_hours for --add --action rm-stale-children")
	fs.StringVar(&o.forget, "forget", "", "remove PATH from the config file")
	fs.IntVar(&o.logTail, "log-tail", 0, "print the last N audit log lines")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: oos [-c|--check] [-k|--known] [-C|--cleanup] [-d|--diff] [-s|--show] [-S|--scan DIR] [-A|--audit DIR]")
		fmt.Fprintln(stderr, "           [-t|--types LIST] [-f|--config FILE] [-y|--yes] [-n|--no] [-q|--quick] [-N|--notify]")
		fmt.Fprintln(stderr, "           [-j|--json] [-v|--verbose] [-i|--init] [-m|--min-mb N] [-V|--version]")
		fmt.Fprintln(stderr, "           [--purge] [--purge-now] [--restore BATCH] [--install-agent] [--uninstall-agent]")
		fmt.Fprintln(stderr, "           [-Q|--quiet] [-F|--free] [-E|--ensure GB] [-W|--why PATH] [--warn GB] [--critical GB]")
		fmt.Fprintln(stderr, "           [--add PATH --type T --action A [--command C] [--note N] [--stale-hours H]] [--forget PATH] [--log-tail N]")
		fmt.Fprintln(stderr)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if o.auditHome && o.audit == "" {
		o.audit = "~"
	}
	modes := 0
	for _, m := range []bool{o.check, o.known, o.cleanup, o.show, o.diff, o.scan != "", o.initCfg,
		o.installAgent, o.uninstallAgent, o.purge, o.purgeNow, o.restore != "", o.audit != "", o.free, o.why != "", o.add != "", o.forget != "", o.logTail > 0, o.ensure > 0, o.ver} {
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
		if err != flag.ErrHelp {
			fmt.Fprintln(stderr, "oos:", err)
		}
		return exitUsage
	}
	if o.ver {
		fmt.Fprintf(stdout, "oos %s\n", version)
		return exitOK
	}
	env, err := realEnv()
	if err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return exitUsage
	}
	if o.initCfg {
		return doInit(env, stdout, stderr)
	}
	if o.installAgent || o.uninstallAgent {
		return doAgent(env, o, stdout, stderr)
	}
	if o.add != "" {
		return doAdd(env, o, stdout, stderr)
	}
	if o.forget != "" {
		return doForget(env, o, stdout, stderr)
	}
	cfg, src, err := loadConfig(o.config, env.Home)
	if err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return exitUsage
	}
	if err := applyOverrides(cfg, o); err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return exitUsage
	}
	if o.quiet {
		stdout = io.Discard
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
	if o.free {
		worst(doFree(cfg, o, stdout, stderr))
	}
	if o.why != "" {
		worst(doWhy(cfg, env, o, stdout, stderr))
	}
	if o.logTail > 0 {
		worst(doLogTail(cfg, o, stdout, stderr))
	}
	if o.check || o.known {
		worst(doCheck(cfg, env, o, now, stdout, stderr))
	}
	if o.diff {
		worst(doDiff(cfg, env, o, now, stdout, stderr))
	}
	if o.scan != "" {
		worst(doScan(cfg, o, now, stdout, stderr))
	}
	if o.audit != "" {
		worst(doAudit(cfg, env, o, now, stdout, stderr))
	}
	if o.restore != "" {
		worst(doRestore(cfg, o, stdout, stderr))
	}
	if o.cleanup {
		worst(doCleanup(cfg, env, o, now, stdout, stderr))
	}
	if o.purge || o.purgeNow {
		worst(doPurge(cfg, o, now, stdout, stderr))
	}
	if o.ensure > 0 {
		worst(doEnsure(cfg, env, o, now, stdout, stderr))
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

func doAgent(env Env, o *opts, stdout, stderr io.Writer) int {
	if o.uninstallAgent {
		if err := agentUninstall(env.Home, agentRun); err != nil {
			fmt.Fprintln(stderr, "oos:", err)
			return exitUsage
		}
		fmt.Fprintln(stdout, "hourly check removed")
		return exitOK
	}
	exe, err := os.Executable()
	if err == nil {
		if r, e2 := filepath.EvalSymlinks(exe); e2 == nil {
			exe = r
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "oos: cannot resolve own path:", err)
		return exitUsage
	}
	if err := agentInstall(env.Home, exe, agentRun); err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return exitUsage
	}
	for p := range agentFiles(env.Home, exe) {
		fmt.Fprintf(stdout, "wrote %s\n", p)
	}
	fmt.Fprintf(stdout, "hourly check installed: %s --check --quick --notify\n", exe)
	return exitOK
}

func doShow(cfg *Config, src string, o *opts, out io.Writer) int {
	st, _ := loadState(cfg.Policy.StateFile)
	batches, _ := listBatches(cfg.Policy.QuarantineDir)
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"config_source": src, "config": cfg, "state": st, "quarantine": batches})
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
	fmt.Fprintf(out, "  quarantine          %v", p.Quarantine)
	if p.Quarantine {
		fmt.Fprintf(out, "  %s, %d days", p.QuarantineDir, p.QuarantineDays)
	}
	fmt.Fprintln(out)
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
	if p.Quarantine {
		if len(batches) == 0 {
			fmt.Fprintln(out, "quarantine: empty")
		} else {
			fmt.Fprintf(out, "quarantine (%d batches, %s):\n", len(batches), human(quarantineBytes(p.QuarantineDir)))
			for _, b := range batches {
				age := time.Since(b.Created).Round(time.Hour)
				expiry := "expires in " + (time.Duration(p.QuarantineDays)*24*time.Hour - age).Round(time.Hour).String()
				if age >= time.Duration(p.QuarantineDays)*24*time.Hour {
					expiry = "EXPIRED, --purge --yes removes it"
				}
				if b.Count < 0 {
					expiry = "no manifest, never auto-purged"
				}
				fmt.Fprintf(out, "  %s  %9s  %3d paths  %s\n", b.Name, human(b.Bytes), b.Count, expiry)
			}
		}
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
	if e.StaleAfterHours > 0 {
		extra += fmt.Sprintf("  stale_after=%dh", e.StaleAfterHours)
	}
	fmt.Fprintf(out, "  %-8s %-18s %s%s\n", e.Type, e.Action, e.Path, extra)
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
		items = buildPlan(cfg, env, splitTypes(o.types), now)
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
	if o.notify && code != exitOK {
		msg := fmt.Sprintf("%.1f GB free of %.1f GB (%s)", du.FreeGB(), du.TotalGB(), label)
		if err := notifyFn("oos: disk "+label, msg); err != nil {
			fmt.Fprintf(errw, "oos: notify: %v\n", err)
		}
	}
	qBytes := int64(0)
	if cfg.Policy.Quarantine {
		qBytes = quarantineBytes(cfg.Policy.QuarantineDir)
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{
			"volume": cfg.Volume, "free_gb": du.FreeGB(), "total_gb": du.TotalGB(),
			"status": label, "quarantine_bytes": qBytes, "known": planJSON(items),
		})
		return code
	}
	fmt.Fprintf(out, "oos %s  %s\n", strings.ToLower(label), cfg.Volume)
	fmt.Fprintf(out, "  free %.1f GB of %.1f GB (%.1f%%)   warn < %.0f GB   critical < %.0f GB\n",
		du.FreeGB(), du.TotalGB(), du.FreePct(), cfg.Policy.WarnFreeGB, cfg.Policy.MinFreeGB)
	if qBytes > 0 {
		fmt.Fprintf(out, "  quarantine holds %s; --purge --yes frees expired batches, --purge-now --yes frees all\n", human(qBytes))
	}
	if o.quick {
		fmt.Fprintln(out, "  (quick: known entries not sized; drop --quick or use --known)")
		return code
	}
	fmt.Fprintf(out, "known entries (%d, largest first):\n", len(items))
	var reclaimable int64
	for _, it := range items {
		mark := " "
		if it.Refused == nil && isDestructive(it.Action) {
			mark = "*"
			reclaimable += it.Deletable
		} else if it.Refused == nil {
			mark = "c"
		}
		size := human(it.Bytes)
		if it.Action == ActionRmStaleChilds && it.Refused == nil {
			size = fmt.Sprintf("%s (%s stale)", human(it.Bytes), human(it.Deletable))
		}
		fmt.Fprintf(out, "  %s %9s  %-8s %-18s %s\n", mark, size, it.Type, it.Action, it.Path)
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
		m := map[string]any{"path": it.Path, "type": it.Type, "action": it.Action, "bytes": it.Bytes, "deletable": it.Deletable}
		if it.Refused != nil {
			m["refused"] = it.Refused.Error()
		}
		if len(it.Children) > 0 {
			m["children"] = it.Children
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
	items := buildPlan(cfg, env, splitTypes(o.types), now)
	// Sizes are expensive; keep them even on a dry-run so --show and --diff have something to say.
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
	if o.jsonOut {
		return doCleanupJSON(cfg, env, o, items, live, now, out, errw)
	}
	var planned int64
	mode := "dry-run"
	if live {
		mode = "LIVE"
	}
	if cfg.Policy.Quarantine {
		mode += ", quarantine"
	} else {
		mode += ", permanent delete"
	}
	fmt.Fprintf(out, "cleanup plan (%s):\n", mode)
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
		case ActionRmStaleChilds:
			planned += it.Deletable
			kept := 0
			for _, c := range it.Children {
				if c.Keep != "" {
					kept++
				}
			}
			fmt.Fprintf(out, "  stale  %9s  %s  (%s of %s; %d kept)\n", human(it.Deletable), it.Path, human(it.Deletable), human(it.Bytes), kept)
			if o.verbose {
				for _, c := range it.Children {
					if c.Keep != "" {
						fmt.Fprintf(out, "         keep   %9s  %s  %s\n", human(c.Bytes), filepath.Base(c.Path), c.Keep)
					} else {
						fmt.Fprintf(out, "         delete %9s  %s  last modified %s\n", human(c.Bytes), filepath.Base(c.Path), c.ModTime.Format("2006-01-02 15:04"))
					}
				}
			}
		default:
			planned += it.Deletable
			fmt.Fprintf(out, "  delete %9s  %s  (%s)\n", human(it.Deletable), it.Path, it.Action)
		}
	}
	fmt.Fprintf(out, "  planned removals: %s   budget: %.0f GB\n", human(planned), cfg.Policy.MaxDeleteGBPerRun)
	if !live {
		fmt.Fprintln(out, "dry-run: nothing touched. Add --yes to execute.")
		return exitOK
	}
	logf, err := openLog(cfg.Policy.LogFile)
	if err != nil {
		fmt.Fprintf(errw, "oos: cannot open log %s: %v; refusing to act without an audit log\n", cfg.Policy.LogFile, err)
		return exitCritical
	}
	defer logf.Close()
	before, _ := diskUsage(cfg.Volume)
	x := &Executor{Policy: cfg.Policy, Log: logf, Out: out, Now: time.Now, Run: shellRun, Move: os.Rename, Refs: env.references}
	if cfg.Policy.Quarantine {
		q, err := openQuarantine(cfg.Policy.QuarantineDir, now, os.Rename)
		if err != nil {
			fmt.Fprintf(errw, "oos: cannot open quarantine %s: %v; refusing to act\n", cfg.Policy.QuarantineDir, err)
			return exitCritical
		}
		x.Q = q
	}
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
	if x.Q != nil {
		fmt.Fprintf(out, "done: %s moved to quarantine batch %s; volume free %.1f GB -> %.1f GB\n", human(freed), x.Q.Batch, before.FreeGB(), after.FreeGB())
		fmt.Fprintf(out, "      space returns on --purge --yes after %d days, or --purge-now --yes; undo with --restore %s\n", cfg.Policy.QuarantineDays, x.Q.Batch)
	} else {
		fmt.Fprintf(out, "done: %s removed; volume free %.1f GB -> %.1f GB\n", human(freed), before.FreeGB(), after.FreeGB())
	}
	_, code := status(cfg.Policy, after)
	return code
}

func doRestore(cfg *Config, o *opts, out, errw io.Writer) int {
	if !cfg.Policy.Quarantine {
		fmt.Fprintln(errw, "oos: quarantine is not enabled in policy")
		return exitUsage
	}
	logf, err := openLog(cfg.Policy.LogFile)
	if err != nil {
		fmt.Fprintf(errw, "oos: cannot open log: %v\n", err)
		return exitCritical
	}
	defer logf.Close()
	n, skipped, err := restoreBatch(cfg.Policy.QuarantineDir, o.restore, os.Rename)
	fmt.Fprintf(logf, "%s restore batch=%s restored=%d skipped=%d err=%v\n", time.Now().UTC().Format(time.RFC3339), o.restore, n, len(skipped), err)
	if err != nil {
		fmt.Fprintf(errw, "oos: restore %s: %v\n", o.restore, err)
		return exitCritical
	}
	fmt.Fprintf(out, "restored %d paths from batch %s\n", n, o.restore)
	for _, s := range skipped {
		fmt.Fprintf(out, "  left in quarantine: %s\n", s)
	}
	return exitOK
}

func doPurge(cfg *Config, o *opts, now time.Time, out, errw io.Writer) int {
	if !cfg.Policy.Quarantine {
		fmt.Fprintln(errw, "oos: quarantine is not enabled in policy")
		return exitUsage
	}
	live := o.yes && !o.no
	olderThan := time.Duration(cfg.Policy.QuarantineDays) * 24 * time.Hour
	batches, err := listBatches(cfg.Policy.QuarantineDir)
	if err != nil {
		fmt.Fprintf(errw, "oos: list quarantine: %v\n", err)
		return exitUsage
	}
	var planned int64
	for _, b := range batches {
		eligible := o.purgeNow || (b.Count >= 0 && now.Sub(b.Created) >= olderThan)
		if eligible {
			planned += b.Bytes
			fmt.Fprintf(out, "  purge  %9s  %s  (%d paths, %s old)\n", human(b.Bytes), b.Name, b.Count, now.Sub(b.Created).Round(time.Hour))
		} else {
			fmt.Fprintf(out, "  keep   %9s  %s  (%d paths, %s old)\n", human(b.Bytes), b.Name, b.Count, now.Sub(b.Created).Round(time.Hour))
		}
	}
	fmt.Fprintf(out, "  would free %s permanently\n", human(planned))
	if !live {
		fmt.Fprintln(out, "dry-run: nothing purged. Add --yes to execute.")
		return exitOK
	}
	logf, err := openLog(cfg.Policy.LogFile)
	if err != nil {
		fmt.Fprintf(errw, "oos: cannot open log: %v; refusing to purge without an audit log\n", err)
		return exitCritical
	}
	defer logf.Close()
	freed, names, err := purgeBatches(cfg.Policy.QuarantineDir, olderThan, now, o.purgeNow)
	fmt.Fprintf(logf, "%s purge all=%v freed=%d batches=%s err=%v\n", now.UTC().Format(time.RFC3339), o.purgeNow, freed, strings.Join(names, ","), err)
	if err != nil {
		fmt.Fprintf(errw, "oos: purge: %v\n", err)
		return exitCritical
	}
	fmt.Fprintf(out, "purged %d batches, %s freed\n", len(names), human(freed))
	return exitOK
}

func splitTypes(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, ",")
}
