# Changelog

## Unreleased

### Robustness for fleets (second review round)

Read before upgrading:
- Exit codes changed: 4 = busy (lock held, retry), 5 = partial (some done, some refused or failed), 6 = I/O. A busy lock and a partial cleanup used to exit 2; a run that did nothing because everything was refused still exits 2 (`error_kind: refused`). JSON outputs gained `kind` and `error_kind`; cleanup `refused` is now a list of `{path, reason}`.
- The Linux timer is `OnCalendar=hourly` with up to 15 minutes' random delay.
- On Linux, `rm-stale-children` and `rm-contents` refuse inside containers and on `hidepid` hosts, where other processes cannot be seen.
- Rolling back to 0.7.x while quarantine batches are held is unsafe: older releases may purge them.

Fixed:
- 782631b protected all of `/root` and `/var/lib` on Linux without exceptions, so `deploy/oos.server.json` was rejected and root could clean nothing under `~`. Both stay protected as wholes, with a short list of cache anchors carved out on path boundaries (`/root/.cache`, `.npm`, `.cargo/registry`, `go/pkg/mod`, `.gradle/caches`, `.m2/repository`, and `/var/lib/docker` as an exact command anchor that can never be emptied). A test loads the shipped server config.
- Security: running as root no longer chowns an existing lock file. A user could hardlink `mutation.lock` or a store's `.oos-store.lock` to a system file and have a root run hand it to them. Lock files are created exclusively and only a freshly created one is handed to the home or store owner; a hardlinked, symlinked or foreign-owned lock is refused. The lock directory is created and chowned by descriptor, closing a swap race.
- A full disk no longer stops oos from freeing space: an 8 MiB `.oos-reserve` is released to write the audit line, and permanent removals fall back to auditing on stderr. Quarantine moves still refuse.
- `--ensure` has separate plan and execution time budgets, reports the phase that timed out, and returns a partial result. Scan budgets are per entry and name `policy.scan_max_entries`. Every skipped or refused entry is a step. btrfs subvolumes and ZFS datasets count as the same volume.
- A tombstone that cannot be deleted no longer blocks `--ensure` or later purges; purge checks deletability first and holds the batch instead.
- A failed intent write, or a move that is proven complete, no longer pins a batch forever. An empty batch left by a crash is removed.
- Batches roll over at 500 entries or an 8 MiB manifest; each take syncs only the batch and store directories. Content checks use a map instead of a nested scan.
- Batches with implausible timestamps (clock errors) are held instead of expired. Non-UTF-8 paths are refused for quarantine.
- A per-store flock (`.oos-store.lock`) stops a root daemon and a user CLI sharing a store from purging a batch mid-restore. Lock files created by root in another user's home are chowned to that user.
- A panic in a planning worker is a refusal, not a daemon crash. Commands run in their own process group, killed whole on timeout. Audit log paths are quoted so a newline in a file name cannot forge a line.
- A rejected config at daemon startup runs the daemon alert-only (embedded thresholds, auto-act off) with `config_error` in `--status`, instead of exiting into a systemd start-limit. The agent notifies once a day. Unknown-key errors say the binary may be older than the config.
- `-j` is honoured by every mode, including purge, restore and quarantine verification; errors never go to stdout. Non-UTF-8 paths also carry `path_b64`.
- The auto-act brake no longer treats any EAGAIN as a busy lock, or a failed `.auto-act.json` write as a reason to pause. A timed-out run is judged by what it recovered. Unsupported no-replace rename is a refusal, alerted once.
- The daemon purges quarantine at most hourly, interruptibly, and not at all when there are no batches. Ticks and the sized walk are jittered; the macOS agent waits up to 10 minutes at random before acting.
- Logs are RFC3339 UTC, rotate at 50 MiB (three kept), and are no longer written twice on macOS. `--log-tail` reads from the end. Old temp files and extra `.corrupt-*` copies are swept.
- The state lock waits at most 10 seconds. The daemon holds `<socket>.lock`, so two daemons cannot both run, and it never removes a successor's socket.
- `--fleet` validates targets, has a hard per-host deadline and `--fleet-parallel N`. `deploy.sh` builds per host architecture.
- Linux process listing reads `/proc` (no `ps` needed, so busybox and slim images work). Unreadable processes and container PID namespaces make the reference check refuse, but other users' processes are counted, not fatal, for non-root runs. `ps`/`lsof` calls have a 30 s limit and `lsof` uses `-b -w`. The daemon's open-file sampling has a deadline. Reference paths match across `/private` aliases and case on macOS.
- 32-bit builds compile. Linux free space uses `f_frsize`. systemd units tolerate a missing `/home`. The sized check no longer lists empty directories on ext4.
- The full suite was run in Linux containers as a user and as root; five Linux-only test failures were fixed (one was a real bug in the space report).
- Exit 7 `nothing_actionable`: a live cleanup whose every work entry is refused by policy at plan time, or a live purge that removed nothing because every batch is held, exits 7 instead of 0. Execution-time refusals and failed deletes keep 2 and 5. Some refused or held beside work done is still 0. An unsafe store lock is now 6 `io` on purge, restore and recover, as on cleanup. The dry-run "other users' processes" note, never filled before execution, is gone; `--ensure 0.1` prints its target as 0.1 GB; `-s` labels a batch whose manifest exists but fails to verify `manifest invalid: <reason>` instead of `no manifest`.

Safety review: every path a cleanup, restore, purge or the daemon can take
was reviewed for data loss, OS/boot damage and integrity, and fixed below.

### Changed (read before upgrading)
- `allow_commands` now defaults to false. Enable it in your config if you rely on `command` entries.
- `oos.json` in the working directory is no longer loaded; pass it with `--config`.
- `--purge-now --yes` keeps held batches and says why; add `--include-held` to delete them too.
- The Linux default's `/var/lib/docker` command entry is now `never`. On macOS, the default `/Library/Developer/CoreSimulator` and Docker `vms/0/data` command entries are refused by the home and `never_touch` rules and show as refused in plans.

### Security
- A config file (explicit or default) must be owned by the effective user and not group/other-writable; the check is made on the opened file. `--add`/`--forget` refuse such files too.
- Command entries must pass the home, `never_touch` and protected-path rules. The command-text scan is case-insensitive, drops quotes, expands `$HOME`/`${HOME}`/`~`/`~user`, cleans `.`, `..` and `//`, reads `/private/...` and `/System/Volumes/Data/...` spellings, checks `never_touch`, and refuses a command naming `/` or the home itself (`rm -rf /`, `find / -delete`, `rm -rf ~/*`). It remains advisory.
- The built-in protected list grew and is per-platform: macOS `/Library` system state (Keychains, LaunchDaemons, LaunchAgents, Preferences, Extensions, TCC), `/Applications`, `/private/etc`, `/private/var/{db,root,vm,protected}`, Preboot/Recovery, iCloud Drive, CloudStorage, Mail, Messages, MobileSync backups, Photos libraries; Linux `/efi`, `/var/lib`, `/root`, `/opt`, `/proc`, `/sys`, `/dev`, `/run`, `/snap`, `/nix`, keyrings; credential stores (`.netrc`, `.git-credentials`, `.docker`, `.password-store`, gcloud, azure, gh) on both.
- Restore re-applies removal-time protections (always_disallowed, never_touch, home/allow_outside_home) to every manifest target before writing anything, refuses stores owned by another uid, and recreates missing parents 0700.
- Every removal refuses paths that overlap oos's own quarantine stores, state, log, size cache or mutation lock, through case and inode aliases.
- Daemon alerts pass their text to `osascript` as arguments, so a file name can no longer inject AppleScript.

### Fixed
- Staleness uses the newest mtime/ctime anywhere in a child's subtree (bounded, no symlinks, no mount crossing); a walk that fails keeps the child. The execution recheck uses the same rule.
- `rm-contents` keeps children a running process references and refuses the entry when references cannot be listed.
- Linux package managers and `softwareupdate` freeze disk changes; `--empty-trash` checks for a running install first.
- Quarantine expiry ignores a source path recreated after quarantine (apps recreate caches at once); only a pending, missing, changed or unrecorded quarantined object holds a batch. Restore still refuses to overwrite a recreated path.
- A take aborted by cancellation or a changed source rolls back its journal intent instead of pinning the batch. Stale `.manifest-*` temps left by a crash no longer hold a batch.
- Purge renames a batch to a `.purging-` tombstone before deleting it; a later purge finishes interrupted deletions.
- An unreadable state file no longer crashes the daemon and agent into a restart loop; it is reported and never overwritten. A panicking tick is recovered. launchd uses `KeepAlive {SuccessfulExit=false}` and systemd `Restart=on-failure` with a start limit; both run at background priority.
- SIGTERM at logout/shutdown reaches ensure, which stops at a safe point. The auto-act cooldown survives a restart; a busy mutation lock is not counted as a failure.
- A second daemon exits cleanly instead of running as a duplicate. The socket path is only removed when it is a socket.
- Installed agents and daemons record the stable `oos` path, not the versioned Homebrew Cellar path, so `brew upgrade` no longer breaks them; plist values are XML-escaped and systemd `ExecStart` is quoted.
- State and size-cache writes use a unique temp file, fsync and rename; read-modify-write of state holds a lock, so concurrent CLI/daemon/agent runs cannot tear or lose them.
- `--ensure` and daemon auto-act steps and log lines say "permanently deleted"; the dry-run counts only expired batches.
- Refuse symlink ancestors and removal trees containing protected paths; use directory handles and mount checks for deletion, including Linux bind mounts and trash emptying. Read-only cleanup no longer changes permissions on hardlinked regular files.
- Bypass cached sizes for destructive planning and remeasure before execution; enforce the remaining budget during removal and across ensure steps. Recheck live guards and each stale child's references, age, and identity. Report execution and audit-write failures.
- Allocate quarantine batches exclusively, persist move intent before rename, recover pending moves, and preserve unrecorded files on restore/discard.
- Open and verify the audit log before ensure purges, include expiry in its deletion budget, and measure actual free space after purging.

### Filesystem integrity
- Serialize cooperating cleanup writers and use atomic no-overwrite quarantine/restore moves.
- Add checksummed v2 journals, identity checks, optional content hashes, read-only verification and metadata-only recovery.
- Support explicitly configured per-volume quarantine stores and a central index.
- Replace unsafe APFS first-block clone inference with conservative accounting (the first-block behavior described under 0.6.2 is superseded); add bounded Linux FIEMAP probes and filesystem diagnostics.
- Add cooperative cancellation, scan/read limits, per-filesystem concurrency and a persisted daemon recovery brake.
- Add a disposable Linux VM fixture and crash-checkpoint tests, isolated behind guest markers and build tags. Runtime filesystem/power-loss validation is still pending.

## [0.7.1] - 2026-09-26

### Added
- `always_disallowed` is the hard-coded refusal list: the operating system (`/System`, `/usr` except `/usr/local`, `/bin`, `/sbin`, `/etc`, `/boot`, the package databases, `macOS Install Data`, and the rest in `protect.Builtin`) plus `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.kube`, and `~/Library/Keychains`. `policy.always_disallowed` only adds paths. Clearing it, clearing `never_touch`, or setting `allow_outside_home` does not lift a hard-coded path. A command whose text names one of them is refused. `--show` prints the hard-coded list. A non-empty macOS upgrade payload, or a running `osinstallersetupd`, `InstallAssistant`, `startosinstall` or `installer`, refuses the disk change. `softwareupdated` does not, because it is always running. If the process list cannot be read, the change is refused.
- A check says when inodes are nearly exhausted, which is the usual reason an app reports "No space left on device" while `df` still shows free bytes. The purgeable line says the same about space `df` counts that an app is not allowed to spend. After a permanent delete or a purge, oos calls `sync` before it measures free space again so that number matches a following `df`. `sync` does not free snapshot blocks, files a process still has open, or anything that was only renamed into quarantine.

## [0.7.0] - 2026-09-26

### Added
- Application safety classes. `--app-leftovers` marks Caches, Logs, Saved Application State, WebKit and HTTPStorages as disposable and suggests `rm-contents`. Application Support, Containers, Group Containers, Preferences and LaunchAgents are keep: an orphan is suggested as `never`, and the folder is not offered for removal. Inside a keep folder, cache directories (Cache, Code Cache, GPUCache, cache2, CacheStorage and the other cache names) are suggested on their own; Bookmarks, Cookies, Login Data, places.sqlite, logins.json and the other user-data files are reported and never suggested. `--who` prints the same class for a path under ~/Library.
- macOS invisible space on a sized `--check`. Time Machine snapshots and `com.apple.os.update-*` snapshots are listed apart; oos still never deletes a snapshot, because the running system may be one of the update snapshots. Purgeable bytes are the CacheDelete estimate. `/Library/Updates`, `macOS Install Data`, `Install macOS*.app` and `$TMPDIR` are sized and explained. Nothing in this section is removed.
- `--trash` measures `~/.Trash` and each volume's `.Trashes/<uid>` (freedesktop trash on Linux). `--empty-trash --yes` deletes the contents permanently. That is not quarantine: there is no restore, a symlink bin is refused, a symlink inside the bin is unlinked rather than followed, and the run is refused without an audit log or when it exceeds the per-run budget.
- File signatures. An extensionless file is classified from its magic bytes at any size, HEIC and AVIF `ftyp` brands count as images, and on a file of at least 1 MB a strong signature wins over an extension that disagrees. A text-or-binary guess still does not override an extension. `--scan`, `--by-type` and `--downloads` show a picture's capture time and camera when EXIF is present (JPEG, a PNG eXIf chunk, or an Exif block in the first 256 KB). The label is not an input to a delete decision.

## [0.6.2] - 2026-09-09

### Fixed
- Sizes on APFS counted every clone in full. uv installs by clone, so its archive of 1087 entries was recorded at 151.1 GB, `--purge-now` printed "151.1 GB freed", and the volume gained 14. Destructive entries from 1 GB up are now also measured clone-aware (`size.Unique`: hardlinks by inode everywhere, large regular files on darwin by the device offset of their first block via `F_LOG2PHYS`; files under 128 KB count per copy, so the figure is an upper bound). `Item.Reclaimable` carries it; the check's `* reclaimable` line, `--cleanup`'s plan (`volume gets back about X`), the JSON plan (`reclaimable`, `reclaimable_bytes`) and the daemon's sized summary use it, and rows whose removal returns less than they record are marked `[shared: ~X reclaimable]`. For `rm-stale-children` the kept children are walked first so a stale clone of a kept entry counts as nothing. The measurement is cached beside the size cache (`sizes.unique.json`) against the deletable total it was taken for, and remeasured when that total moves, after the TTL, or with `--fresh`.
- `--purge` reports the manifest total as recorded and the volume's free space before and after, which is what actually came back; the dry run says "up to". `--cleanup` labels its total the same way. The audit log carries both numbers.
- A live `--cleanup` whose only work was commands left an empty quarantine batch behind (20260908-210611 on the Mac). An empty batch is discarded at the end of the run and the summary says "nothing quarantined"; the JSON form drops `batch`.

## [0.6.1] - 2026-09-08

### Added
- `--app-leftovers`: every entry in the ~/Library areas apps write into, paired with an installed app by bundle id (helpers included) or name. Orphans first with `--add` lines tagged `leftover`; unmatched names softened as probable tool caches; `com.apple` never judged; `-v` lists installed entries, which is per-app cache sizing. On this Mac: 2.2 GB of orphans, 294 apps seen, 8 s.
- macOS default config: Xcode simulators (`xcrun simctl delete unavailable`, command), iOS DeviceSupport (rm-contents), Xcode Archives and MobileSync backups (`never`, so they are shown and kept).

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
