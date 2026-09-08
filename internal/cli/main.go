// oos: out of space. Tracks known large directories and files, reports disk
// headroom against a policy, and cleans up only what the config says is safe,
// only with --yes, only inside the per-run budget, and only after every guard
// passes. Anything it cannot prove safe, it refuses.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/afterdarksys/oos/internal/snapshots"
	"github.com/afterdarksys/oos/internal/status"

	"github.com/afterdarksys/oos/internal/agent"
	"github.com/afterdarksys/oos/internal/audit"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/docker"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/state"
)

const Version = "0.5.0"

type opts struct {
	check, known, cleanup, show, diff, quick, yes, no, jsonOut, verbose bool
	initCfg, notify, installAgent, uninstallAgent, purge, purgeNow, ver bool
	scan, types, config, restore, audit                                 string
	minMB                                                               int64

	// script-facing
	quiet, free, auditHome, agentTick, system, fresh, permanent bool
	ensure, warnGB, critGB                                      float64
	why, add, forget, addType, addAction                        string
	addCommand, addNote, addUseCase, who                        string
	addStale, logTail, history                                  int

	// filters shared by --scan, --audit, --by-type, --dupes
	olderThan, newerThan, sortBy, ext, tag, addTags, byType string
	top                                                     int

	// --scan-builds
	scanBuilds string
	depth      int

	// --fleet
	fleet bool
	hosts string

	// --dupes DIR, --downloads [DIR]
	dupes, downloads string
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
	fs.StringVar(&o.who, "who", "", "what PATH is for and who is using it: use case, repo, fingerprint, referencing processes, newest file")
	fs.StringVar(&o.addUseCase, "use-case", "", "use_case label for --add")
	fs.BoolVar(&o.agentTick, "agent-tick", false, "one scheduled tick: quick check, growth alert, expired-quarantine purge, notification")
	fs.BoolVar(&o.system, "system", false, "with --install-agent/--uninstall-agent on Linux: system-wide units in /etc/systemd/system (root)")
	fs.BoolVar(&o.fresh, "fresh", false, "ignore stored sizes for this run and refresh the size cache from what is measured")
	fs.BoolVar(&o.permanent, "permanent", false, "with --cleanup --yes: delete outright instead of quarantining (space returns immediately, no undo)")
	fs.IntVar(&o.history, "history", 0, "print the last N free-space readings from the state file")
	fs.StringVar(&o.olderThan, "older-than", "", "with --scan/--audit/--by-type/--dupes: only entries not modified in AGE (90d, 2w, 36h, 6mo, 1y)")
	fs.StringVar(&o.newerThan, "newer-than", "", "with --scan/--audit/--by-type/--dupes: only entries modified within AGE")
	fs.StringVar(&o.sortBy, "sort", "", "with --scan/--audit: size (default), oldest, newest or name")
	fs.IntVar(&o.top, "top", 0, "with --scan/--audit/--by-type: show at most N rows")
	fs.StringVar(&o.ext, "ext", "", "with --scan/--by-type/--dupes: comma-separated extensions (dmg,iso,log,tar.gz)")
	fs.StringVar(&o.byType, "by-type", "", "size every regular file under DIR by type (disk image, archive, video, log, ...)")
	fs.StringVar(&o.tag, "tag", "", "only entries carrying TAG (--check/--known/--cleanup: entry tags; --audit: automatic tags like stale-1y, build-output, repo)")
	fs.StringVar(&o.addTags, "tags", "", "comma-separated tags for --add")
	fs.StringVar(&o.scanBuilds, "scan-builds", "", "size every git repo under DIR as source, .git and build output; suggest --add lines for build dirs of clean repos idle --older-than (default 30d)")
	fs.IntVar(&o.depth, "depth", 0, "with --scan-builds: how many levels down to look for repos (default 4)")
	fs.BoolVar(&o.fleet, "fleet", false, "ask every host in policy.fleet (or --hosts) for its quick check over ssh and print one table")
	fs.StringVar(&o.hosts, "hosts", "", "with --fleet: comma-separated ssh targets instead of policy.fleet")
	fs.StringVar(&o.dupes, "dupes", "", "list identical files under DIR (size, then head/tail hash, then SHA-256); --min-mb floor, default 10")
	fs.StringVar(&o.downloads, "downloads", "", "judge a downloads folder (DIR, or 'default' for ~/Downloads): installed installers, extracted archives, copies, partials, apps, stale")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: oos [-c|--check] [-k|--known] [-C|--cleanup] [-d|--diff] [-s|--show] [-S|--scan DIR] [-A|--audit DIR]")
		fmt.Fprintln(stderr, "           [-t|--types LIST] [-f|--config FILE] [-y|--yes] [-n|--no] [-q|--quick] [-N|--notify]")
		fmt.Fprintln(stderr, "           [-j|--json] [-v|--verbose] [-i|--init] [-m|--min-mb N] [-V|--version]")
		fmt.Fprintln(stderr, "           [--purge] [--purge-now] [--restore BATCH] [--install-agent] [--uninstall-agent]")
		fmt.Fprintln(stderr, "           [-Q|--quiet] [-F|--free] [-E|--ensure GB] [-W|--why PATH] [--warn GB] [--critical GB]")
		fmt.Fprintln(stderr, "           [--add PATH --type T --action A [--command C] [--note N] [--stale-hours H] [--use-case U]] [--forget PATH] [--log-tail N]")
		fmt.Fprintln(stderr, "           [--who PATH] [--agent-tick] [--install-agent [--system]]")
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
		o.installAgent, o.uninstallAgent, o.purge, o.purgeNow, o.restore != "", o.audit != "", o.free, o.why != "", o.add != "", o.forget != "", o.logTail > 0, o.ensure > 0, o.who != "", o.agentTick, o.history > 0, o.ver, o.byType != "", o.scanBuilds != "", o.fleet, o.dupes != "", o.downloads != ""} {
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

func Run(args []string, stdout, stderr io.Writer) int {
	o, err := parseFlags(args, stderr)
	if err != nil {
		if err != flag.ErrHelp {
			fmt.Fprintln(stderr, "oos:", err)
		}
		return status.ExitUsage
	}
	if o.ver {
		fmt.Fprintf(stdout, "oos %s\n", Version)
		return status.ExitOK
	}
	env, err := guard.Real()
	if err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return status.ExitUsage
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
	cfg, src, err := config.Load(o.config, env.Home)
	if err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return status.ExitUsage
	}
	if err := applyOverrides(cfg, o); err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return status.ExitUsage
	}
	if o.quiet {
		stdout = io.Discard
	}
	if cfg.Policy.SizeCacheFile != "" {
		ttl := time.Duration(cfg.Policy.SizeCacheHours * float64(time.Hour))
		if ttl <= 0 {
			ttl = 6 * time.Hour
		}
		size.Active = size.OpenCache(cfg.Policy.SizeCacheFile, ttl)
		size.Active.Refresh = o.fresh // measure everything, then store it: the next run is warm and honest
		defer func() {
			if err := size.Active.Save(); err != nil {
				fmt.Fprintf(stderr, "oos: save size cache: %v\n", err)
			}
			if o.verbose {
				h, m := size.Active.Stats()
				fmt.Fprintf(stderr, "size cache: %d hits, %d misses\n", h, m)
			}
		}()
	}
	if !cfg.Policy.ReferenceOpenFiles {
		env.Open = nil
	}
	now := time.Now()
	code := status.ExitOK
	worst := func(c int) {
		if c > code {
			code = c
		}
	}
	if o.show {
		worst(doShow(cfg, src, o, stdout))
	}
	if o.agentTick {
		return agent.Tick(cfg, o.jsonOut, now, stdout, stderr)
	}
	if o.free {
		worst(doFree(cfg, o, stdout, stderr))
	}
	if o.who != "" {
		worst(doWho(cfg, env, o, now, stdout, stderr))
	}
	if o.why != "" {
		worst(doWhy(cfg, env, o, stdout, stderr))
	}
	if o.logTail > 0 {
		worst(doLogTail(cfg, o, stdout, stderr))
	}
	if o.history > 0 {
		worst(doHistory(cfg, o, stdout))
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
	if o.byType != "" {
		worst(doByType(cfg, env, o, now, stdout, stderr))
	}
	if o.scanBuilds != "" {
		worst(doScanBuilds(cfg, env, o, now, stdout, stderr))
	}
	if o.fleet {
		worst(doFleet(cfg, o, stdout, stderr))
	}
	if o.dupes != "" {
		worst(doDupes(cfg, env, o, now, stdout, stderr))
	}
	if o.downloads != "" {
		worst(doDownloads(cfg, env, o, now, stdout, stderr))
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

func doInit(env guard.Env, stdout, stderr io.Writer) int {
	dst := filepath.Join(env.Home, ".config", "oos", "oos.json")
	if _, err := os.Stat(dst); err == nil {
		fmt.Fprintf(stderr, "oos: %s already exists; not overwriting\n", dst)
		return status.ExitUsage
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return status.ExitUsage
	}
	if err := os.WriteFile(dst, config.Default, 0o644); err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return status.ExitUsage
	}
	fmt.Fprintf(stdout, "wrote %s\n", dst)
	return status.ExitOK
}

func doAgent(env guard.Env, o *opts, stdout, stderr io.Writer) int {
	if o.uninstallAgent {
		if err := agent.Uninstall(env.Home, o.system, agent.Exec); err != nil {
			fmt.Fprintln(stderr, "oos:", err)
			return status.ExitUsage
		}
		fmt.Fprintln(stdout, "hourly check removed")
		return status.ExitOK
	}
	exe, err := os.Executable()
	if err == nil {
		if r, e2 := filepath.EvalSymlinks(exe); e2 == nil {
			exe = r
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "oos: cannot resolve own path:", err)
		return status.ExitUsage
	}
	if err := agent.Install(env.Home, exe, o.system, agent.Exec); err != nil {
		fmt.Fprintln(stderr, "oos:", err)
		return status.ExitUsage
	}
	files := agent.Files(env.Home, exe)
	if o.system {
		files = agent.SystemFiles(exe)
	}
	for p := range files {
		fmt.Fprintf(stdout, "wrote %s\n", p)
	}
	fmt.Fprintf(stdout, "hourly agent installed: %s --agent-tick\n", exe)
	return status.ExitOK
}

func doShow(cfg *config.Config, src string, o *opts, out io.Writer) int {
	st, _ := state.Load(cfg.Policy.StateFile)
	batches, _ := plan.ListBatches(cfg.Policy.QuarantineDir)
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"config_source": src, "config": cfg, "state": st, "quarantine": batches})
		return status.ExitOK
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
			fmt.Fprintf(out, "quarantine (%d batches, %s):\n", len(batches), size.Human(plan.QuarantineBytes(p.QuarantineDir)))
			for _, b := range batches {
				age := time.Since(b.Created).Round(time.Hour)
				expiry := "expires in " + (time.Duration(p.QuarantineDays)*24*time.Hour - age).Round(time.Hour).String()
				if age >= time.Duration(p.QuarantineDays)*24*time.Hour {
					expiry = "EXPIRED, --purge --yes removes it"
				}
				if b.Count < 0 {
					expiry = "no manifest, never auto-purged"
				}
				fmt.Fprintf(out, "  %s  %9s  %3d paths  %s\n", b.Name, size.Human(b.Bytes), b.Count, expiry)
			}
		}
	}
	return status.ExitOK
}

func printEntry(out io.Writer, e config.Entry) {
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
	if len(e.Tags) > 0 {
		extra += "  [" + strings.Join(e.Tags, " ") + "]"
	}
	fmt.Fprintf(out, "  %-8s %-18s %s%s\n", e.Type, e.Action, e.Path, extra)
	if e.Note != "" {
		fmt.Fprintf(out, "           %s\n", e.Note)
	}
}

func doCheck(cfg *config.Config, env guard.Env, o *opts, now time.Time, out, errw io.Writer) int {
	du, err := size.Disk(cfg.Volume)
	if err != nil {
		fmt.Fprintf(errw, "oos: statfs %s: %v\n", cfg.Volume, err)
		return status.ExitUsage
	}
	label, code := status.Of(cfg.Policy, du)
	var items []plan.Item
	var dk *docker.Usage
	var dkErr error
	dkRan := false
	var snap *snapshots.Report
	var snapErr error
	snapRan := false
	if !o.quick {
		items = plan.BuildTagged(cfg, env, splitTypes(o.types), o.tag, now)
		if want, forced := docker.Wanted(cfg.Policy); want {
			dkRan = true
			dk, dkErr = docker.Collect(docker.Timeout(cfg.Policy))
			if dkErr != nil && forced {
				fmt.Fprintf(errw, "oos: docker: %v\n", dkErr)
			}
		}
		if want, forced := snapshotsWanted(cfg.Policy); want {
			snapRan = true
			snap, snapErr = snapshots.Collect(cfg.Volume)
			if snapErr != nil && forced {
				fmt.Fprintf(errw, "oos: snapshots: %v\n", snapErr)
			}
		}
	}
	st, _ := state.Load(cfg.Policy.StateFile)
	st.Volume = cfg.Volume
	for _, it := range items {
		if it.Refused == nil || it.Bytes > 0 {
			st.Known[it.Path] = it.Bytes
		}
	}
	st.Record("check", du, now)
	fc := st.Forecast(now, cfg.Policy.ForecastWindow(), cfg.Policy.WarnFreeGB, cfg.Policy.MinFreeGB)
	if err := state.Save(cfg.Policy.StateFile, st); err != nil {
		fmt.Fprintf(errw, "oos: save state: %v\n", err)
	}
	if o.notify && code != status.ExitOK {
		msg := fmt.Sprintf("%.1f GB free of %.1f GB (%s)", du.FreeGB(), du.TotalGB(), label)
		if err := agent.Notify("oos: disk "+label, msg); err != nil {
			fmt.Fprintf(errw, "oos: notify: %v\n", err)
		}
	}
	qBytes := int64(0)
	if cfg.Policy.Quarantine {
		qBytes = plan.QuarantineBytes(cfg.Policy.QuarantineDir)
	}
	if o.jsonOut {
		j := map[string]any{
			"volume": cfg.Volume, "free_gb": du.FreeGB(), "total_gb": du.TotalGB(),
			"status": label, "quarantine_bytes": qBytes, "known": planJSON(items), "by_use_case": checkUseCases(cfg, items),
			"version": Version, "forecast": fc,
		}
		if snapRan {
			if snapErr != nil {
				j["snapshots"] = map[string]any{"error": snapErr.Error()}
			} else {
				j["snapshots"] = snap
			}
		}
		if dkRan {
			j["docker"] = docker.JSON(dk, dkErr)
		}
		_ = json.NewEncoder(out).Encode(j)
		return code
	}
	fmt.Fprintf(out, "oos %s  %s\n", strings.ToLower(label), cfg.Volume)
	fmt.Fprintf(out, "  free %.1f GB of %.1f GB (%.1f%%)   warn < %.0f GB   critical < %.0f GB\n",
		du.FreeGB(), du.TotalGB(), du.FreePct(), cfg.Policy.WarnFreeGB, cfg.Policy.MinFreeGB)
	if qBytes > 0 {
		fmt.Fprintf(out, "  quarantine holds %s; --purge --yes frees expired batches, --purge-now --yes frees all\n", size.Human(qBytes))
	}
	if fc.Falling() {
		fmt.Fprintf(out, "  %s\n", fc.String())
	}
	if o.quick {
		fmt.Fprintln(out, "  (quick: known entries not sized; drop --quick or use --known)")
		return code
	}
	fmt.Fprintf(out, "known entries (%d, largest first):\n", len(items))
	var reclaimable int64
	for _, it := range items {
		mark := " "
		if it.Refused == nil && config.IsDestructive(it.Action) {
			mark = "*"
			reclaimable += it.Deletable
		} else if it.Refused == nil {
			mark = "c"
		}
		sz := size.Human(it.Bytes)
		if it.Action == config.ActionRmStaleChilds && it.Refused == nil {
			sz = fmt.Sprintf("%s (%s stale)", size.Human(it.Bytes), size.Human(it.Deletable))
		}
		fmt.Fprintf(out, "  %s %9s  %-8s %-18s %s\n", mark, sz, it.Type, it.Action, it.Path)
		if o.verbose && it.Refused != nil {
			fmt.Fprintf(out, "                refused: %v\n", it.Refused)
		}
	}
	fmt.Fprintf(out, "  * reclaimable by --cleanup --yes now: %s   c = via command\n", size.Human(reclaimable))
	if dkRan {
		if dkErr != nil {
			fmt.Fprintf(out, "docker: skipped (%v)\n", dkErr)
		} else {
			docker.Report(out, dk, o.verbose)
		}
	}
	if snapRan {
		if snapErr != nil {
			fmt.Fprintf(out, "local snapshots: skipped (%v)\n", snapErr)
		} else {
			snapshots.Print(out, snap, o.verbose)
		}
	}
	paths := make([]string, len(items))
	sizes := make([]int64, len(items))
	for i, it := range items {
		paths[i], sizes[i] = it.Path, it.Bytes
	}
	audit.PrintUseCaseTotals(out, audit.GroupByUseCase(cfg, paths, sizes))
	return code
}

func planJSON(items []plan.Item) []map[string]any {
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

func doScan(cfg *config.Config, o *opts, now time.Time, out, errw io.Writer) int {
	root := filepath.Clean(o.scan)
	minMB := o.minMB
	if minMB <= 0 {
		minMB = cfg.Policy.BigFileMinMB
	}
	f, err := filterFromOpts(o)
	if err != nil {
		fmt.Fprintln(errw, "oos:", err)
		return status.ExitUsage
	}
	// scan wide, then filter, sort and cap: the cap must apply after the window
	hits, err := size.ScanBig(root, minMB*1024*1024, 0)
	if err != nil {
		fmt.Fprintf(errw, "oos: scan %s: %v\n", root, err)
		return status.ExitUsage
	}
	kept := hits[:0]
	for _, h := range hits {
		if f.KeepTime(h.ModTime, now) && f.KeepName(h.Path) {
			kept = append(kept, h)
		}
	}
	hits = kept
	sort.SliceStable(hits, size.LessFor(f.SortBy,
		func(i int) int64 { return hits[i].Bytes },
		func(i int) time.Time { return hits[i].ModTime },
		func(i int) string { return hits[i].Path }))
	hits = hits[:size.CapRows(len(hits), f.Top, cfg.Policy.ScanTopN)]
	st, _ := state.Load(cfg.Policy.StateFile)
	st.BigFiles = hits
	st.ScanRoot = root
	st.ScannedAt = now
	if du, err := size.Disk(cfg.Volume); err == nil {
		st.Record("scan", du, now)
	}
	if err := state.Save(cfg.Policy.StateFile, st); err != nil {
		fmt.Fprintf(errw, "oos: save state: %v\n", err)
	}
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(hits)
		return status.ExitOK
	}
	fmt.Fprintf(out, "big files under %s (>= %d MB, %d shown%s):\n", root, minMB, len(hits), f.Describe())
	for _, h := range hits {
		fmt.Fprintf(out, "  %9s  %s  %s\n", size.Human(h.Bytes), h.ModTime.Format("2006-01-02"), h.Path)
	}
	fmt.Fprintf(out, "recorded %d files in %s\n", len(hits), cfg.Policy.StateFile)
	return status.ExitOK
}

func doCleanup(cfg *config.Config, env guard.Env, o *opts, now time.Time, out, errw io.Writer) int {
	live := o.yes && !o.no
	if !cfg.Policy.RequireYes {
		live = !o.no
	}
	items := plan.BuildTagged(cfg, env, splitTypes(o.types), o.tag, now)
	// Sizes are expensive; keep them even on a dry-run so --show and --diff have something to say.
	if st, err := state.Load(cfg.Policy.StateFile); err == nil {
		st.Volume = cfg.Volume
		for _, it := range items {
			if it.Bytes > 0 {
				st.Known[it.Path] = it.Bytes
			}
		}
		if du, err := size.Disk(cfg.Volume); err == nil {
			st.Record("plan", du, now)
		}
		_ = state.Save(cfg.Policy.StateFile, st)
	}
	if o.jsonOut {
		return doCleanupJSON(cfg, env, o, items, live, now, out, errw)
	}
	var planned int64
	mode := "dry-run"
	if live {
		mode = "LIVE"
	}
	if cfg.Policy.Quarantine && !o.permanent {
		mode += ", quarantine"
	} else {
		mode += ", permanent delete"
	}
	fmt.Fprintf(out, "cleanup plan (%s):\n", mode)
	for _, it := range items {
		if it.Refused != nil {
			fmt.Fprintf(out, "  skip   %9s  %s\n", size.Human(it.Bytes), it.Path)
			if o.verbose || it.Action != config.ActionNever {
				fmt.Fprintf(out, "         %v\n", it.Refused)
			}
			continue
		}
		switch it.Action {
		case config.ActionCommand:
			fmt.Fprintf(out, "  run    %9s  %s  -> %q\n", size.Human(it.Bytes), it.Path, it.Command)
		case config.ActionRmStaleChilds:
			planned += it.Deletable
			kept := 0
			for _, c := range it.Children {
				if c.Keep != "" {
					kept++
				}
			}
			fmt.Fprintf(out, "  stale  %9s  %s  (%s of %s; %d kept)\n", size.Human(it.Deletable), it.Path, size.Human(it.Deletable), size.Human(it.Bytes), kept)
			if o.verbose {
				for _, c := range it.Children {
					if c.Keep != "" {
						fmt.Fprintf(out, "         keep   %9s  %s  %s\n", size.Human(c.Bytes), filepath.Base(c.Path), c.Keep)
					} else {
						fmt.Fprintf(out, "         delete %9s  %s  last modified %s\n", size.Human(c.Bytes), filepath.Base(c.Path), c.ModTime.Format("2006-01-02 15:04"))
					}
				}
			}
		default:
			planned += it.Deletable
			fmt.Fprintf(out, "  delete %9s  %s  (%s)\n", size.Human(it.Deletable), it.Path, it.Action)
		}
	}
	fmt.Fprintf(out, "  planned removals: %s   budget: %.0f GB\n", size.Human(planned), cfg.Policy.MaxDeleteGBPerRun)
	if !live {
		fmt.Fprintln(out, "dry-run: nothing touched. Add --yes to execute.")
		return status.ExitOK
	}
	logf, err := state.OpenLog(cfg.Policy.LogFile)
	if err != nil {
		fmt.Fprintf(errw, "oos: cannot open log %s: %v; refusing to act without an audit log\n", cfg.Policy.LogFile, err)
		return status.ExitCritical
	}
	defer logf.Close()
	before, _ := size.Disk(cfg.Volume)
	x := &plan.Executor{Policy: cfg.Policy, Log: logf, Out: out, Now: time.Now, Run: plan.ShellRun, Move: os.Rename, Refs: env.References}
	if cfg.Policy.Quarantine && !o.permanent {
		q, err := plan.OpenQuarantine(cfg.Policy.QuarantineDir, now, os.Rename)
		if err != nil {
			fmt.Fprintf(errw, "oos: cannot open quarantine %s: %v; refusing to act\n", cfg.Policy.QuarantineDir, err)
			return status.ExitCritical
		}
		x.Q = q
	}
	fmt.Fprintln(out, "executing:")
	freed, err := x.Execute(items)
	if err != nil {
		fmt.Fprintf(errw, "oos: refused: %v\n", err)
		return status.ExitCritical
	}
	after, _ := size.Disk(cfg.Volume)
	st, _ := state.Load(cfg.Policy.StateFile)
	st.Record("cleanup", after, now)
	_ = state.Save(cfg.Policy.StateFile, st)
	if x.Q != nil {
		fmt.Fprintf(out, "done: %s moved to quarantine batch %s; volume free %.1f GB -> %.1f GB\n", size.Human(freed), x.Q.Batch, before.FreeGB(), after.FreeGB())
		fmt.Fprintf(out, "      space returns on --purge --yes after %d days, or --purge-now --yes; undo with --restore %s\n", cfg.Policy.QuarantineDays, x.Q.Batch)
	} else {
		fmt.Fprintf(out, "done: %s removed; volume free %.1f GB -> %.1f GB\n", size.Human(freed), before.FreeGB(), after.FreeGB())
	}
	_, code := status.Of(cfg.Policy, after)
	return code
}

func doRestore(cfg *config.Config, o *opts, out, errw io.Writer) int {
	if !cfg.Policy.Quarantine {
		fmt.Fprintln(errw, "oos: quarantine is not enabled in policy")
		return status.ExitUsage
	}
	logf, err := state.OpenLog(cfg.Policy.LogFile)
	if err != nil {
		fmt.Fprintf(errw, "oos: cannot open log: %v\n", err)
		return status.ExitCritical
	}
	defer logf.Close()
	n, skipped, err := plan.RestoreBatch(cfg.Policy.QuarantineDir, o.restore, os.Rename)
	fmt.Fprintf(logf, "%s restore batch=%s restored=%d skipped=%d err=%v\n", time.Now().UTC().Format(time.RFC3339), o.restore, n, len(skipped), err)
	if err != nil {
		fmt.Fprintf(errw, "oos: restore %s: %v\n", o.restore, err)
		return status.ExitCritical
	}
	fmt.Fprintf(out, "restored %d paths from batch %s\n", n, o.restore)
	for _, s := range skipped {
		fmt.Fprintf(out, "  left in quarantine: %s\n", s)
	}
	return status.ExitOK
}

func doPurge(cfg *config.Config, o *opts, now time.Time, out, errw io.Writer) int {
	if !cfg.Policy.Quarantine {
		fmt.Fprintln(errw, "oos: quarantine is not enabled in policy")
		return status.ExitUsage
	}
	live := o.yes && !o.no
	olderThan := time.Duration(cfg.Policy.QuarantineDays) * 24 * time.Hour
	batches, err := plan.ListBatches(cfg.Policy.QuarantineDir)
	if err != nil {
		fmt.Fprintf(errw, "oos: list quarantine: %v\n", err)
		return status.ExitUsage
	}
	var planned int64
	for _, b := range batches {
		eligible := o.purgeNow || (b.Count >= 0 && now.Sub(b.Created) >= olderThan)
		if eligible {
			planned += b.Bytes
			fmt.Fprintf(out, "  purge  %9s  %s  (%d paths, %s old)\n", size.Human(b.Bytes), b.Name, b.Count, now.Sub(b.Created).Round(time.Hour))
		} else {
			fmt.Fprintf(out, "  keep   %9s  %s  (%d paths, %s old)\n", size.Human(b.Bytes), b.Name, b.Count, now.Sub(b.Created).Round(time.Hour))
		}
	}
	fmt.Fprintf(out, "  would free %s permanently\n", size.Human(planned))
	if !live {
		fmt.Fprintln(out, "dry-run: nothing purged. Add --yes to execute.")
		return status.ExitOK
	}
	logf, err := state.OpenLog(cfg.Policy.LogFile)
	if err != nil {
		fmt.Fprintf(errw, "oos: cannot open log: %v; refusing to purge without an audit log\n", err)
		return status.ExitCritical
	}
	defer logf.Close()
	freed, names, err := plan.PurgeBatches(cfg.Policy.QuarantineDir, olderThan, now, o.purgeNow)
	fmt.Fprintf(logf, "%s purge all=%v freed=%d batches=%s err=%v\n", now.UTC().Format(time.RFC3339), o.purgeNow, freed, strings.Join(names, ","), err)
	if err != nil {
		fmt.Fprintf(errw, "oos: purge: %v\n", err)
		return status.ExitCritical
	}
	fmt.Fprintf(out, "purged %d batches, %s freed\n", len(names), size.Human(freed))
	return status.ExitOK
}

// snapshotsWanted mirrors docker.Wanted: policy.snapshots true forces the
// tmutil question and reports a failure, false disables it, unset means
// "on macOS when tmutil exists".
func snapshotsWanted(p config.Policy) (want, forced bool) {
	if p.Snapshots != nil {
		return *p.Snapshots, *p.Snapshots
	}
	return snapshots.Supported(), false
}

func splitTypes(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func checkUseCases(cfg *config.Config, items []plan.Item) []audit.UseCaseTotal {
	paths := make([]string, len(items))
	sizes := make([]int64, len(items))
	for i, it := range items {
		paths[i], sizes[i] = it.Path, it.Bytes
	}
	return audit.GroupByUseCase(cfg, paths, sizes)
}
