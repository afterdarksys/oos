# Changelog

## [0.6.0] - 2026-09-08

### Added
- Daemon: `--daemon` runs the resident watcher, `--install-daemon` / `--uninstall-daemon` keep it under launchd or systemd (`--system`), replacing the hourly agent. Every `interval_minutes` (5) it records free space, fits the forecast, samples open files and names what grew, and alerts on status change, drop (with writers), projected critical crossing and recovery, rate-limited to `alert_repeat_minutes`. Every `sized_every_hours` (6) it sizes known entries and asks Docker. `--status` asks it over a unix socket and answers during long walks; without a daemon it reads the state file. Acting is off unless `policy.daemon.auto_act` is true, then under critical it runs the `--ensure` path toward `auto_act_target_gb` no more often than the cooldown, every guard re-checked and everything logged. SIGHUP reloads, SIGTERM stops.
- `--ensure` now runs through `plan.Ensure`, the same code the daemon uses.
- The state history keeps 1000 readings (about three and a half days of five-minute ticks).

### Changed
- Repository layout: one `package main` of forty files became `cmd/oos` plus `internal/{cli,config,size,guard,plan,state,audit,docker,repos,agent,status,testutil}`, each owning one concern. No behaviour change; the suite moved with the code and runs per package.
- `build.sh`: `build`, `install`, `test` (gofmt + vet + full suite, exit code gated), `linux`, `clean`, `all`. Pins the asdf Go version so builds work from any directory.

### Added
- Filters shared by `--scan`, `--audit` and `--by-type`: `--older-than` / `--newer-than AGE` (`36h`, `90d`, `2w`, `6mo`, `1y`, bare days), `--ext LIST` (compound extensions and rotated logs understood), `--sort size|oldest|newest|name`, `--top N`.
- `--by-type DIR`: every regular file bucketed by category with the largest files in each; extension first, magic bytes for anything over 1 MB without one.
- Tags: `tags` on entries (`--add --tags`), `--tag` narrows `--check`, `--known`, `--cleanup` and `--audit`; audit rows carry automatic tags (`stale-30d/90d/180d/1y`, `big`, `huge`, `hidden`, `build-output`, `repo`) plus their status and entry tags.
- `--dupes DIR`: identical files by size, head-and-tail hash, then full SHA-256; hardlinks are one file; groups most wasted first with the newest copy marked; `--min-mb` floor (default 10). On this Mac's Downloads: 3.4 GB of exact repeats in 38 s.
- `--downloads [DIR]`: verdicts for a downloads folder: `installed` (installer whose app is in /Applications), `installer`, `extracted`, `copy`, `copy-differs`, `partial`, `app-in-downloads`, `stale`, `big`, with bytes per verdict. Labels only.
- Time Machine local snapshots (macOS) in a sized `--check`: count, dates and the `tmutil thinlocalsnapshots` command; `policy.snapshots` forces on or off. Never run by oos.
- Forecast: the free-space history is fitted over `forecast_window_hours` (default 6) into a GB/hour rate and hours to the warn and critical lines; `--history`, `--check` and the tick show it, JSON carries it as `forecast`, and the tick alerts when critical is inside `alert_hours_to_critical` (default 24, negative disables). Three readings over an hour are required; less says so.
- `--fleet` (`policy.fleet`, or `--hosts a,b`): every host's quick check over ssh in one table, least free first, unreachable hosts last with the error; exit is the worst host. `fleet_timeout_seconds` (default 20). The check JSON now carries `version`.
- `--scan-builds DIR`: every git repo under DIR sized as source, `.git` and build output by fingerprint (`target`+Cargo.toml, `node_modules`+package.json, `.next`, `dist`, venvs, `.terraform`, Gradle, Pods, .NET and more), with last commit, newest source change and git's clean/dirty word. Build dirs of clean repos idle for `--older-than` (default 30d) are emitted as `--add --action rm-contents` lines tagged `build-output,repo:<name>`; anything else says why it is kept. `--depth` bounds the search; nested repos are scanned on their own.

## [0.5.0] - 2026-09-08

### Added
- Docker-aware sizing: a sized `--check` asks the daemon (`docker system df`, `docker volume ls -f dangling=true`) and prints images, containers, build cache and volumes with what each prune command would return, plus the dangling volumes, sized when their mountpoint is on this host. Dangling volumes are refused, never pruned. `policy.docker` (unset: when `docker` is on the PATH), `policy.docker_timeout_seconds` (default 120; the daemon took ten minutes to answer on a host with a hundred containers). JSON output carries a `docker` section, or its error. `--quick` never asks.
- Fleet: `deploy/deploy.sh` dry-run now builds the linux/amd64 binary and probes every host read-only (kernel, free space, installed version, config, timer, `docker`/`logger` present); an unreachable host stops the run before anything ships. First real Linux run: dry-run against apps.afterdarksys.com.
- Linux notify falls back to `logger -t oos -p user.warning` when `notify-send` is absent, so a headless server's alerts land in syslog instead of an error on every tick.

### Changed
- `--fresh` refreshes the size cache instead of disabling it: every directory is remeasured and the results are stored, so the run after a `--fresh` is warm with honest numbers. Before, a stale entry survived a `--fresh` run untouched.

### Fixed
- `deploy.sh` ended each host with `oos -F`, whose exit code is the disk status; under `set -e` a host in warn or critical made a successful deploy report failure. Status lines no longer abort the script.
- `--install-agent --system` printed the user-unit paths although it wrote the root units in `/etc/systemd/system`; seen on the first live deploy (relay-b).
- `--history` printed a GB/day trend over a window of seconds ("-239.2 GB/day over 0s" on the first apps deploy); a window under an hour now says it needs more readings.
- The probe's last `command -v` returned nonzero when `notify-send` was absent, which made the whole probe fail.

## [0.4.1] - 2026-09-08

### Fixed
- 0.4.0 was tagged with a failing size-cache test: the load-time prune ran before the test lowered the floor. The floor is now a parameter of the cache constructor. No behaviour change for users.

## [0.4.0] - 2026-09-08

### Added
- Hierarchical size cache keyed by directory mtime with a TTL (`size_cache_file`, `size_cache_hours`, `--fresh`). Only directories over 4 MB are stored, since a hit on a parent covers its children. Measured on a 700 GB home directory: 362 s cold, about 11 s warm.
- Open file descriptors count as process references (`reference_open_files`).
- Deep attribution: unlabelled audit rows are opened up to two levels and reported as `mixed: ...`, with totals spread across components.
- `--permanent`: one cleanup run that deletes instead of quarantining.
- `--history N`: free-space readings and GB/day trend; history is kept sorted by time.
- End-to-end test that spawns real processes referencing archive children by argv, cwd and open file, then runs the real listers and executor.

### Fixed
- macOS `ps` clipped long command lines, so a path deep in an argument list could be missed as a reference; now `ps -ww`.
- `lsof` runs with `-n -P`, cutting the open-file listing from 16 s to 2 s.
- The tool no longer counts its own process as a reference.
- Owner globs support `**` across directories.


## [0.3.0] - 2026-09-08

### Added
- Use-case attribution: `use_case` on entries, `policy.owners` path and glob patterns, and automatic attribution from on-disk fingerprints (Cargo.toml, package.json, pyvenv.cfg, DerivedData, .terraform, .git, enclosing repo). `--check` and `--audit` group totals by use case; `--add --use-case`.
- `--who PATH`: attribution, referencing processes (command line and cwd), and the newest file under the path.
- `--agent-tick`: the hourly job is now one cheap tick that notifies under warn or on a free-space drop of `alert_drop_gb` since the previous tick, and purges expired quarantine batches when `agent_purge_expired` is set. Agent files call it.
- `--install-agent --system` on Linux: root units in `/etc/systemd/system` with a hardening block.
- Fleet: `deploy/deploy.sh` (dry-run by default, no `--force`), `deploy/oos.server.json` for Linux servers, `.vpscfgfarm.map`.


## [0.2.0] - 2026-09-08

### Added
- `rm-stale-children` action: per-child cleanup of caches that live processes run from. Keeps children referenced by any process command line or working directory, children inside a `stale_after_hours` floor, symlinks and lock files; re-checks references immediately before each removal; refuses when processes cannot be listed.
- Quarantine: rm actions move into `quarantine_dir/<batch>/` with a manifest. `--restore BATCH`, `--purge` (expired batches), `--purge-now` (all), and the batch is shown in `--show` and `--check`. Cross-device entries are refused in the plan.
- `--diff`: growth of known entries and free space since the last recorded sizes, with a hint when the drop is outside known entries.
- `--install-agent` / `--uninstall-agent`: hourly `--check --quick --notify` via launchd (macOS) or a systemd user timer (Linux). `--notify` posts a desktop notification when status is not OK.
- Linux support: `/proc/*/cwd` references, `notify-send`, systemd units, and an embedded `oos.linux.json` default selected at build time.
- `--audit DIR`: one-level home directory audit with known/protected/system/unknown status and hints (cache-like names, git repos, stale, hidden hogs).
- Script-facing modes: `--quiet`, `--free`, `--ensure GB`, `--why PATH`, `--warn`/`--critical` overrides, `--add`/`--forget` config editing with validation, `--log-tail N`, and JSON output for `--cleanup`.
- `--version`.
- Config validation now rejects nested destructive entries and entries overlapping the log, state or quarantine paths.

### Changed
- The macOS default config uses `rm-stale-children` for `~/.cache/uv/archive-v0` instead of refusing it, and adds the Rust target dirs and `~/.codex` as `never` entries so they show up in reports.
- The per-run budget counts what would actually be removed, not the full entry size.

## [0.1.0] - 2026-09-07

Initial release: config-driven known dirs and files, policy guards, dry-run
by default, audit log, `--check --known --cleanup --show --scan`.
