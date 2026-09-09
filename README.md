# oos

Out of space. A small Go tool that knows where the disk goes on a machine,
reports headroom against a policy, and cleans up only what its config says is
safe. It refuses anything it cannot prove safe, and since 0.2.0 every removal
is a move into quarantine that you can undo for a week.

Born 2026-09-07 when a Mac's Data volume hit 3.8 GB free, Docker Desktop
started throwing I/O errors on its own metadata, and the fix was blocked by
the thing it needed to fix. Runs on macOS and Linux.

## Build and install

```sh
go build -o oos .
GOBIN=/usr/local/bin go install .     # not plain `go install` under asdf
```

## Use

```sh
oos                       # quick check: free space vs policy, no sizing
oos -c                    # full check: sizes every known entry (slow on big caches)
oos -d                    # diff: what grew since the last recorded sizes
oos -s                    # show resolved config, policy, state, quarantine batches
oos -S ~/Downloads        # scan for big files, record in bigfile.json
oos -C                    # cleanup plan, dry-run
oos -C -y                 # cleanup for real (into quarantine)
oos -C -y -t cache        # only entries of type cache
oos -C -y -n              # -n wins: still a dry-run
oos --restore 20260908-101500   # undo one batch
oos --purge -y            # permanently delete batches older than quarantine_days
oos --purge-now -y        # permanently delete every batch (the emergency lever)
oos --install-agent       # hourly quick check with a desktop notification under warn
oos -i                    # write the platform default config to ~/.config/oos/oos.json
oos -A                    # audit ~ one level deep: size, age, use case, known/unknown, hints
oos --audit ~/Library -v  # audit somewhere else, show every hint
oos --who ~/.cache/uv     # what is this for, which repo, which processes, newest file
oos --history 24          # free-space readings and the GB/day trend
oos -C -y --permanent     # cleanup that frees space now instead of quarantining
oos -c --fresh            # remeasure everything and refresh the size cache
oos --by-type ~/Downloads                          # disk images 29.7 GB, archives 13.4 GB, ...
oos -S ~/Downloads --ext dmg,iso --older-than 90d --sort oldest --top 10
oos -A --tag stale-1y                             # only entries untouched for a year
oos --add ~/x --type cache --action rm-contents --tags build-output,review
oos -C -t cache --tag build-output                # cleanup plan narrowed by tag
oos --scan-builds ~/development                   # build output per repo, guarded --add lines
oos --dupes ~/Downloads -m 200                    # identical files over 200 MB, newest copy marked
oos --downloads                                   # installed installers, extracted archives, copies, stale
oos --history 12                                  # readings, trend and the forecast
oos --fleet                                       # every host in policy.fleet in one table
oos --install-daemon && oos --status              # resident watcher; ask it any time
```

## In scripts and for agents

oos is meant to be the thing a script or an agent asks before it touches a
disk. Every mode is exit-code first and has `-j` JSON.

```sh
oos -Q --critical 20 || exit 1        # pre-flight: fail if under 20 GB, print nothing
[ "$(oos -F)" -ge 50 ] || oos -E 50 -y # bare integer GB; make room if needed
oos -W ~/.cache/foo                    # would oos act on this? 0 yes, 1 unknown but allowed, 2 refused
oos -W ~/.cache/foo -j | jq .verdict
oos --add ~/.cache/foo --type cache --action rm-contents --note "seen 2026-09-08"
oos --forget ~/.cache/foo
oos -C -j | jq '.plan[] | select(.refused == null) | .path'
oos --log-tail 20                      # what did the last run actually do
oos -d -j | jq '.rows[0]'              # biggest grower since last time
oos --who ~/x -j | jq .use_case        # attribution for a path
oos -A -j | jq '.by_use_case'          # home directory grouped by what the bytes are for
```

`--ensure GB` is the pre-flight lever: it purges expired quarantine batches,
then removes configured entries largest first, stopping as soon as the target
is met. Removals under `--ensure` are permanent, because the caller asked for
space it can use now; they are still logged. `--warn` and `--critical`
override the thresholds for one run so a script can hold its own standard.

`--add` and `--forget` edit the config file in place (`--config`, else the
first of `./oos.json` and `~/.config/oos/oos.json`, seeding the latter from
the embedded default if neither exists). The result is validated exactly as
a load would before the file is written, so a bad entry never lands.

Long forms: `--check --known --cleanup --diff --show --scan DIR --audit DIR
--types LIST --config FILE --yes --no --quick --notify --json --verbose --init
--min-mb N --version --purge --purge-now --restore BATCH --install-agent
--uninstall-agent --quiet --free --ensure GB --why PATH --warn GB --critical GB
--add PATH --type T --action A --command C --note N --stale-hours H
--use-case U --forget PATH --log-tail N --who PATH --agent-tick --system`.

Exit codes: 0 ok, 1 free space below warn, 2 below critical or a refusal, 3
usage or config error.

## Config

Lookup order: `--config`, `./oos.json`, `~/.config/oos/oos.json`, then the
copy embedded in the binary (`oos.json` on macOS, `oos.linux.json` on Linux).
Unknown keys are an error so a typo cannot silently weaken the policy.

`known_dirs` and `known_files` entries carry:

| field | meaning |
|---|---|
| `path` | absolute, `~` allowed |
| `type` | free-form label used by `--types` (cache, build, vm, sim, data, scratch) |
| `action` | `never`, `rm-contents` (dirs, keeps the dir), `rm-stale-children` (dirs, see below), `rm` (files), `command` |
| `command` | shell command for `action: command` |
| `stale_after_hours` | for `rm-stale-children`: children modified more recently are kept |
| `use_case` | what the bytes are for; grouped in `--check`, `--audit` and shown by `--who` |
| `guard_processes` | substrings; if any running process matches, the whole entry is refused |
| `note` | why it is here and what to know |

`policy` is the cover-your-ass block:

| field | effect |
|---|---|
| `min_free_gb`, `warn_free_gb` | thresholds for the check status, exit code and notification |
| `require_yes` | without `--yes`, `--cleanup` and `--purge` only print the plan |
| `max_delete_gb_per_run` | the whole run is refused before anything is touched if the plan exceeds this |
| `allow_outside_home` | default false: paths outside `$HOME` are refused |
| `allow_commands` | `action: command` entries are skipped when false |
| `never_touch` | any path equal to or under these is refused, regardless of entry; a bare `/` protects only `/` |
| `min_path_depth` | refuse shallow paths like `/Users/x` |
| `log_file` | append-only audit log; a live run refuses to start if it cannot open it |
| `state_file` | `bigfile.json` |
| `big_file_min_mb`, `scan_top_n` | `--scan` defaults |
| `quarantine`, `quarantine_dir`, `quarantine_days` | move instead of delete; expiry for `--purge` |
| `owners` | list of `{match, use_case, note}`; `match` is a path (covers everything under it) or a glob tried against a path and each ancestor |
| `agent_purge_expired` | the hourly tick releases quarantine batches older than `quarantine_days` |
| `size_cache_file`, `size_cache_hours` | per-directory sizes keyed by mtime, reused within the TTL (default 6h); empty file disables |
| `reference_open_files` | open file descriptors count as process references (one `lsof -n -P` on macOS, `/proc/*/fd` on Linux) |
| `alert_drop_gb` | the tick notifies when free space fell by this much since the previous tick (default 10) |
| `docker`, `docker_timeout_seconds` | ask the daemon during a sized check: unset means when `docker` is on the PATH, `false` never, `true` always and report a silent daemon; the deadline defaults to 120 s |
| `forecast_window_hours`, `alert_hours_to_critical` | window the rate is fitted over (default 6) and how far ahead a projected critical crossing alerts (default 24; negative disables) |
| `fleet`, `fleet_timeout_seconds` | ssh targets `--fleet` asks (`root@host`, or an ssh alias) and the per-host deadline (default 20) |
| `snapshots` | ask `tmutil` for local snapshots during a sized check: unset means on macOS when it exists, `false` never, `true` always |
| `daemon` | `interval_minutes` (5), `socket` (path or `off`), `sized_every_hours` (6, negative never), `alert_repeat_minutes` (60), `find_writer` (true), `writer_top_n` (5), `auto_act` (false), `auto_act_target_gb` (warn line), `auto_act_cooldown_minutes` (60) |

## rm-stale-children

For caches that live processes run out of, such as uv's `archive-v0` where
every `uvx` server keeps its Python environment. Refusing the whole
directory while anything runs means it never gets cleaned. Instead, each
direct child is judged on its own:

- kept if any running process references it, by command line (`ps -ww`, so
  long argument lists are not clipped), by working directory (`lsof -d cwd`
  on macOS, `/proc/*/cwd` on Linux), or by an open file descriptor
  (`lsof -n -P`, `/proc/*/fd`)
- kept if modified inside the `stale_after_hours` floor
- kept if it is a symlink or a lock file
- otherwise a delete candidate

References are gathered again immediately before each removal, so a process
that started between plan and execute keeps its environment. If processes
cannot be listed, the entry is refused rather than guessed.

## Quarantine

With `quarantine: true`, every rm action renames the path into
`quarantine_dir/<batch>/<original absolute path>` and records it in the
batch's `manifest.json`. Space is not freed until the batch is purged, and
the manifest records allocated blocks per file: where files share blocks
(APFS clones, hardlinks) the volume gives back less than the recorded
total, so `--purge` prints both the recorded figure and the volume's free
space before and after. The plan says up front what a removal returns
(`[shared: ~X reclaimable]` on an entry, `volume gets back about X` under
the total) for destructive entries from 1 GB up.
`--restore BATCH` moves everything back and refuses to overwrite anything
that has reappeared. `--purge -y` removes batches older than
`quarantine_days`; `--purge-now -y` removes all of them. A batch without a
manifest is never auto-purged. Rename cannot cross filesystems, so an entry
on a different device from the quarantine dir is refused in the plan.

## Audit

`--audit DIR` (default `~`) sizes every direct child, hidden ones included,
and tags each as known (covered by an entry), protected (under
`never_touch`), system (`Library`, `Applications`), or unknown. Unknown
entries get hints: a name that looks like a cache or build output, a git
repository that belongs in `never_touch`, nothing modified in six months, a
hidden directory over 1 GB. It is a report for the person editing
`oos.json`; it never acts. Results land in `bigfile.json`.

## Size cache

Every directory's size is remembered with its mtime and the time it was
measured. A later walk stops at any directory whose mtime is unchanged and
whose entry is younger than `size_cache_hours`, so churny caches
invalidate exactly where entries were added or removed and untouched
subtrees cost one stat. A file growing in place does not bump a directory
mtime, so the TTL is the backstop for logs, databases and disk images;
`--fresh` ignores every stored size for one run and rewrites the cache from
what it measures, so the run after it is warm again with honest numbers;
`-v` reports hits and misses. Only
directories over 4 MB are stored, because a hit on a parent covers its
children. On a 700 GB home directory a full audit went from 362 s cold to
about 11 s warm.

## Filters, sorting and tags

`--scan`, `--audit` and `--by-type` share one set of windows: `--older-than`
and `--newer-than` take an age (`36h`, `90d`, `2w`, `6mo`, `1y`, or a bare
number of days) and compare it with the file's mtime, or for an audit row
the newest mtime found in the subtree; `--ext dmg,iso,tar.gz` keeps only
those extensions (rotated `app.log.2.gz` still counts as a log, and `gz`
matches every compound that ends in it); `--sort size|oldest|newest|name`
and `--top N` order and cap the rows. `--by-type DIR` buckets every
regular file by category (disk image, archive, package, video, audio,
image, document, log, database, binary, model, source, font) from its
extension, sniffing the first bytes of anything over 1 MB that has none.

Tags are labels. An entry carries the ones you give it (`--add --tags`,
or `tags` in the config), and `--check`, `--known` and `--cleanup` narrow
to one with `--tag`. An audit row carries tags oos can prove from disk:
`stale-30d/90d/180d/1y`, `big` (over 1 GB), `huge` (over 10 GB), `hidden`,
`build-output`, `repo`, plus its status and any entry tags, and
`--audit --tag stale-1y` is the one-line answer to "what have I not
touched in a year".

## Build output inside repos

`--scan-builds DIR` finds every git repository under DIR (to `--depth`,
default 4) and sizes each as source, `.git` and build output: `target`
beside a `Cargo.toml`, `node_modules` beside a `package.json`, `.next`,
`dist`, `build`, a `.venv` with a `pyvenv.cfg` inside, `.terraform`,
`.gradle`, Pods, `bin`/`obj` beside a `.csproj`, and the rest of the
fingerprint list in `repos.go`. A directory only counts as build output
when its fingerprint is there; a `target` with no `Cargo.toml` is source.
Nested repositories are listed and scanned on their own, not folded into
the parent. Each repo shows its last commit, newest source change and
whether git calls it clean. For a clean repo idle for `--older-than`
(default 30d, no commit and no source change) the build directories come
out as ready-to-paste `--add ... --action rm-contents` lines, tagged
`build-output` and `repo:<name>`; a dirty or active repo keeps its bytes
and the line says why. Nothing is added or removed by the scan itself.

## Docker

The daemon's data root is one opaque directory to a file walk, and on
macOS it is inside a VM disk image, so a sized `--check` asks the daemon
instead: `docker system df` for images, containers, build cache and
volumes with what each prune command would return, and
`docker volume ls -f dangling=true` for the volumes nothing references.
Dangling volumes are listed, sized when their mountpoint is on this host,
and refused: on this fleet an "unused" volume has been live data one
character away from the name a container mounts, and
`docker volume prune` would have been data loss. `oos` never runs it.
The section is on when a `docker` binary is on the PATH; `policy.docker`
forces it on or off, and `docker_timeout_seconds` bounds the wait, because
the daemon sizes every container and volume to answer (ten minutes on a
host with a hundred containers). A daemon that does not answer is reported
and skipped; the rest of the check does not depend on it, and `--quick`
never asks.

## Use cases

Paths say where bytes sit; use cases say what they are for. Three sources,
first match wins: the entry's own `use_case`, an `owners` pattern from the
policy, then automatic attribution from what is on disk: a `Cargo.toml`
beside a `target` dir, a `package.json` beside `node_modules`, a
`pyvenv.cfg`, `DerivedData`, `.terraform`, a `.git`, and the enclosing git
repository's name. `--check`, `--audit` and their JSON forms group totals by
use case, with `unattributed` shown rather than hidden. An audit row that
has no label at the top level, such as `~/.cache` or `~/Library`, is opened
up to two levels and reported as `mixed: 61% uv, 30% Homebrew`, and the
totals spread it across those components. `--who PATH` prints
the attribution plus every running process whose command line or working
directory references the path and the newest file under it: the answer to
"who is filling this up right now".

## Diff and agent

`--diff` sizes the known entries and prints growth against the sizes
`bigfile.json` recorded last time, plus the free-space change. If the drop in
free space is bigger than the growth of known entries, it says so: that is
the cue to `--scan` a suspect root.

`--install-agent` writes a launchd agent (macOS, `~/Library/LaunchAgents`) or
a systemd user timer (Linux, `~/.config/systemd/user`) that runs
`oos --agent-tick` hourly. `--install-agent --system` on Linux writes root
units into `/etc/systemd/system` with a `ProtectSystem=strict` block. A tick
is one `statfs` and one state write: it notifies (`osascript` or
`notify-send`) when free space is under the warn line or fell by more than
`alert_drop_gb` since the previous tick within three hours, and with
`agent_purge_expired` it releases quarantine batches past their expiry.

## Daemon

`oos --daemon` is the resident watcher; `--install-daemon` keeps it running
under launchd or systemd (`--system` for root units) and removes the hourly
agent, which it replaces. Every `interval_minutes` (default 5) it records
free space, fits the forecast, samples every open file and names what
grew since the last tick, and alerts: on a status change, once per
`alert_repeat_minutes` while a condition holds, on a drop of
`alert_drop_gb` between ticks (with the writers), on a projected critical
crossing inside `alert_hours_to_critical`, and on recovery. Every
`sized_every_hours` (default 6) it sizes the known entries and asks
Docker, so `--status` always has a recent reclaimable figure.

`oos --status` asks it over the unix socket (`policy.daemon.socket`,
default `~/.local/state/oos/oos.sock`) and answers even while a long walk
is running, showing what the daemon is busy with; without a daemon it
reads the state file and says so. Exit code is the disk status.

Acting is off unless `policy.daemon.auto_act` is true. Then, under
critical and no more often than `auto_act_cooldown_minutes` (default 60),
it runs the same path as `--ensure` toward `auto_act_target_gb` (default
`warn_free_gb`): expired quarantine first, then entries largest first,
inside the per-run budget, every guard re-checked, everything logged and
announced. Each knob has a default, so an empty `daemon` block is a
daemon that only watches. SIGHUP reloads the config; SIGTERM stops it.

## Duplicates, Downloads and snapshots

`--dupes DIR` lists identical files: size first, then a head-and-tail
hash, then a full SHA-256 only for what still matches, so a 50 GB folder
costs one read of the candidates. Hardlinks are one file. Groups come
most wasted first with the newest copy marked; `--min-mb` sets the floor
(default 10) and the age and extension windows apply. Nothing is removed.

`--downloads` (or `--downloads DIR`) judges a downloads folder the way a
person would: `installed` (a dmg or pkg whose app sits in /Applications),
`installer`, `extracted` (an archive with its folder beside it), `copy`
(a "(2)" with the original still there, same size), `copy-differs`,
`partial`, `app-in-downloads`, `stale` (180 days), `big` (1 GB). Rows
with a verdict come first, bytes are totalled per verdict, and the labels
are the whole output.

On macOS a sized `--check` also asks `tmutil` for local Time Machine
snapshots, the space no file walk can see, and prints the count, dates
and the exact `tmutil thinlocalsnapshots` command that releases them.
`policy.snapshots` forces it on or off. oos never runs that command.

## App leftovers

`--app-leftovers` reads the ~/Library areas applications write into
(Application Support, Caches, Containers, Group Containers, Saved
Application State, Logs, WebKit, HTTPStorages, LaunchAgents, Preferences)
and pairs every entry over `--min-mb` (default 10) with an installed app:
by bundle identifier, including helpers such as `com.vendor.app.ShipIt`,
then by name, loosely. An identifier no installed app owns is an `orphan`
and comes out as an `--add` line tagged `leftover`; a plain name nothing
matches is `unmatched`, which on a developer machine is usually a tool
cache (Cypress, pnpm, aws) rather than a leftover; anything under
`com.apple` is never judged. `--tag orphan` narrows, `-v` also lists what
is installed, which is the per-app size of ~/Library/Caches. The macOS
default config also knows Xcode simulators (`xcrun simctl delete
unavailable`), iOS DeviceSupport, Archives and MobileSync backups, the
last two as `never` so they show up and stay.

## Forecast

Every tick and every check records free space, and the history is fitted
with a least-squares line over the last `forecast_window_hours` (default
6). With three readings spanning at least an hour the result is a rate in
GB per hour and, when it is falling, the hours until the warn and critical
lines. `--history` and a sized or quick `--check` print it, the JSON forms
carry it as `forecast`, and the tick alerts when the critical crossing is
inside `alert_hours_to_critical` (default 24; a negative value turns the
rate alert off). Thin data says what it needs instead of guessing.

## Fleet

`oos --fleet` asks every ssh target in `policy.fleet` (or `--hosts a,b`)
for its quick check and prints one table: status, free, total, used,
quarantine, forecast, host, least free first, unreachable hosts last with
the ssh error. The remote command is `oos -c -q -j`, which sizes nothing.
Exit code is the worst host: 3 when one did not answer, else the worst
disk status. `fleet_timeout_seconds` bounds each host (default 20).

`deploy/deploy.sh [--yes] host...` builds linux/amd64, ships the binary,
seeds `~/.config/oos/oos.json` from `deploy/oos.server.json` only when the
host has none (a differing config is left beside it as `oos.json.new`),
installs the system timer and runs a quick check. Dry-run without `--yes`,
and `--force` does not exist. The dry-run still builds and probes every
host read-only (kernel, free space, installed version, config, timer,
whether `docker` and `logger` exist), and a host that cannot be probed stops
the run before anything is shipped anywhere. The server config never
touches `/opt`, `/var/lib/docker/volumes`, database directories or
`/var/log`; Docker is limited to `docker builder prune`. `.vpscfgfarm.map`
routes the repo to that script.

On a headless Linux host there is no desktop to notify: when `notify-send`
is absent the tick writes the alert to syslog through `logger -t oos`, where
the journal and any log shipper pick it up.

## Layout and building

```
cmd/oos/            the binary: three lines that call cli.Run
internal/cli/       flags, every command, all human-facing output
internal/config/    oos.json shape, policy, entries, owners, attribution, validation
internal/size/      statfs, allocated sizes, the size cache, filters, file types
internal/guard/     the refusal rules and process/cwd/open-file references
internal/plan/      plan building, the executor, quarantine
internal/state/     bigfile.json: sizes, scans, audits, free-space history
internal/audit/     directory audits, tags, use-case breakdowns
internal/docker/    docker system df and dangling volumes
internal/repos/     build output inside git repositories
internal/agent/     the hourly tick, launchd/systemd units, notifications
internal/status/    OK/WARN/CRITICAL and the exit codes
internal/testutil/  shared test helpers
deploy/             fleet deploy script and the server config
```

`./build.sh` builds `./oos`; `./build.sh install` puts it in `/usr/local/bin`
(`GOBIN` overrides); `./build.sh test` runs gofmt, vet and the full suite
with the exit code gated; `./build.sh linux` cross-builds for the fleet;
`./build.sh clean` removes the outputs. The script pins the asdf Go
version, which is why `go install` by hand fails from any directory
without a `.tool-versions`.

## Guards, in order

For every destructive entry: not `never`; absolute; not `/`; deep enough;
under home unless allowed; not under `never_touch`; exists; not a symlink;
right kind for the action; no guard process running; same device as the
quarantine dir. Then the run as a whole must fit the byte budget. Symlinks
inside a directory are moved or unlinked as links, never followed. Sizes
never cross onto another device, so a mounted volume inside a tree is not
counted or touched. The config itself refuses nested destructive entries and
any entry that overlaps the log, state or quarantine paths.

## State

`bigfile.json` records the last free-space reading, sizes of known entries,
the last `--scan` hits, and a capped history of readings. It is an
observation cache and is never an input to a delete decision. A corrupt state
file is moved aside, not fatal.

## Threats

The tool deletes files, so the failure modes that matter are deleting the
wrong thing and deleting too much. Controls: home-only by default, an explicit
never-touch list, minimum depth, no symlink following, per-run byte budget
checked before the first removal, process references and an age floor for
shared caches with a re-check at removal time, quarantine with manifests and
restore, dry-run unless `--yes` and `--no` always wins, strict config parsing
that rejects nested and self-overlapping entries, and an audit log that must
be writable before a live run begins. The tests cover each refusal, the
dry-run and symlink cases, stale classification and re-check, quarantine
take, restore, partial restore, expiry and failed moves, diff, notification
gating and agent file generation.
