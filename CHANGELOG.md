# Changelog

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
