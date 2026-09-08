# oos

Out of space. A small Go tool that knows where the disk goes on this machine,
reports headroom against a policy, and cleans up only what its config says is
safe. It refuses anything it cannot prove safe.

Born 2026-09-07 when the Data volume hit 3.8 GB free, Docker Desktop started
throwing I/O errors on its own metadata, and the fix was blocked by the thing
it needed to fix.

## Build

```sh
go build -o oos .
# or
go install .
```

## Use

```sh
oos                       # quick check: free space vs policy, no sizing
oos -c                    # full check: sizes every known entry (slow on big caches)
oos -k                    # same as -c, reads better as "list known"
oos -s                    # show resolved config, policy, and last state
oos -S ~/Downloads        # scan for big files, record in bigfile.json
oos -C                    # cleanup plan, dry-run
oos -C -y                 # cleanup for real
oos -C -y -t cache        # only entries of type cache
oos -C -y -n              # -n wins: still a dry-run
oos -i                    # write the default config to ~/.config/oos/oos.json
```

Long forms: `--check --known --cleanup --show --scan DIR --types LIST
--config FILE --yes --no --quick --json --verbose --init --min-mb N`.

Exit codes: 0 ok, 1 free space below warn, 2 below critical or a refusal, 3
usage or config error.

## Config

Lookup order: `--config`, `./oos.json`, `~/.config/oos/oos.json`, then the
copy embedded in the binary. Unknown keys are an error so a typo cannot
silently weaken the policy.

`known_dirs` and `known_files` entries carry:

| field | meaning |
|---|---|
| `path` | absolute, `~` allowed |
| `type` | free-form label used by `--types` (cache, build, vm, sim, data, scratch) |
| `action` | `never`, `rm-contents` (dirs, keeps the dir), `rm` (files), `command` |
| `command` | shell command for `action: command` |
| `guard_processes` | substrings; if any running process matches, refuse |
| `note` | why it is here and what to know |

`policy` is the cover-your-ass block:

| field | effect |
|---|---|
| `min_free_gb`, `warn_free_gb` | thresholds for the check status and exit code |
| `require_yes` | without `--yes`, `--cleanup` only prints the plan |
| `max_delete_gb_per_run` | the whole run is refused before anything is touched if the plan exceeds this |
| `allow_outside_home` | default false: paths outside `$HOME` are refused |
| `allow_commands` | `action: command` entries are skipped when false |
| `never_touch` | any path equal to or under these is refused, regardless of entry |
| `min_path_depth` | refuse shallow paths like `/Users/x` |
| `log_file` | append-only audit log; a live run refuses to start if it cannot open it |
| `state_file` | `bigfile.json` |
| `big_file_min_mb`, `scan_top_n` | `--scan` defaults |

## Guards, in order

For every `rm` or `rm-contents` entry: not `never`; absolute; not `/`; deep
enough; under home unless allowed; not under `never_touch`; exists; not a
symlink; right kind for the action; no guard process running. Then the run as
a whole must fit the byte budget. Symlinks inside a directory are unlinked,
never followed. Sizes never cross onto another device, so a mounted volume
inside a tree is not counted or touched.

## State

`bigfile.json` records the last free-space reading, sizes of known entries,
the last `--scan` hits, and a capped history of readings. It is an
observation cache and is never an input to a delete decision. A corrupt state
file is moved aside, not fatal.

## Threats

The tool deletes files, so the failure modes that matter are deleting the
wrong thing and deleting too much. Controls: home-only by default, an explicit
never-touch list, minimum depth, no symlink following, per-run byte budget
checked before the first delete, process guards for caches that live servers
run from, dry-run unless `--yes` and `--no` always wins, strict config parsing,
and an audit log that must be writable before a live run begins. The tests
cover each refusal and the dry-run and symlink cases.
