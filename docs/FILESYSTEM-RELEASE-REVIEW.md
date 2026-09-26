# Filesystem integrity release review

## Workstation boundary

Development and ordinary tests use temporary fixtures only. No production
cleanup, daemon installation, host disk formatting, mounts, repair tools, or
crash injection are part of development validation. The privileged/crash matrix
is separately tagged `vmtest` and must run in a disposable guest.

## Implemented for independent review

- A stable, nonblocking per-home mutation lock shared by CLI writers, ensure,
  scheduled purges, and recovery. It is never unlinked on release.
- Platform no-replace rename through pinned source/destination parents:
  Linux `renameat2(RENAME_NOREPLACE)` and macOS `renameatx_np(RENAME_EXCL)`.
  Unsupported operations fail; no check-then-rename fallback.
- Existing protected objects compared through device/inode identity to recognize
  case and Unicode spelling aliases. The explicit `/usr/local` exception stays.
- Version 2 quarantine manifests: operation ID, checksum, source identity and
  metadata, pending state, optional recursive SHA-256 hashes. Version 0 legacy
  manifests remain readable, but cannot prove identity in ambiguous recovery.
- Read-only `--verify-quarantine [--deep]`; metadata-only `--recover BATCH`,
  dry-run unless `--yes`. Conflicts, changed payloads, and unknown files remain.
- Explicit per-volume quarantine mappings and a central store index. No copying
  or automatic directory creation on discovered mounts. Restore/purge/verification
  search configured stores; ambiguous batch IDs require `--quarantine-store`.
- Filesystem capability reporting and optional bounded read-only diagnostics.
  Logical, allocated, reclaimable upper estimate, known exclusive/shared extents,
  unknown ownership, and observed recovery are separate concepts.
- Linux FIEMAP reports known unshared/shared extents and refuses to infer encoded,
  delayed, unknown, or unsupported extent ownership. It never reads/writes block
  devices. APFS first-block clone inference has been removed; ownership remains
  explicitly unknown rather than assigning a misleading whole-file size.
- Uncached safety measurements, per-filesystem scan concurrency, cancellation at
  removal boundaries, operation deadlines, per-context entry/hash-read limits,
  and cross-step deletion byte budgets.
- Persisted automatic-cleanup circuit breaker after repeated errors or poor
  recovery. Normal success cannot silently clear a paused breaker.

## Suggested division for Fable and Grok

Fable: syscall behavior, inode/path alias safety, locking across CLI/daemon,
rename durability, restore races, permissions/ACL/xattr preservation, and the
quarantine state machine. Confirm no ambiguous operation can lead to data loss.

Grok: accounting correctness, sparse files, reflinks, external hardlinks,
subvolume/mount boundaries, report semantics, budgets, timeout behavior,
filesystem-specific failures, and test coverage/portability.

Both should independently inspect the privileged test driver before running it.
Do not infer release readiness from compilation or a skipped test.

## Required release evidence (not yet obtained)

- Linux ext3, ext4, XFS (reflink enabled), and Btrfs disposable-volume matrix.
- macOS HFS+, case-sensitive HFS+, APFS, and case-sensitive APFS image matrix on
  a disposable Mac/VM. Include Unicode aliases, resource forks, ACLs, xattrs,
  sparse files, clones with a modified tail, and snapshot retention.
- Real process termination at each durable journal checkpoint, plus a controlled
  VM power interruption. Unit error injection does not prove power-loss safety.
- ENOSPC during journal creation/update and filesystem metadata exhaustion;
  read-only remount; permission denial; concurrent writers; stale mounts; quota
  limits; deleted-but-open files. These are additional release gates even if the
  supplied baseline matrix passes.
- A file inventory and content/metadata comparison before/after restore. Unknown
  or unrecorded payloads must survive recovery. A manifest checksum is not a
  payload checksum or protection against deliberate manifest tampering.

## Known limits to assess

Locks coordinate cooperating oos processes for the same home; they do not stop
other applications, administrators, or separate users. Path/metadata changes can
still occur immediately after a check. Extent results are observations, not a
promise that blocks will become free. APFS exclusive extent ownership is not
available through this implementation. Deep verification adds I/O and does not
hash ACLs or all platform-specific attributes; those require round-trip tests.
Per-filesystem capability names describe available probe methods, not guaranteed
support for every file or kernel. ext2/3/4 share a filesystem magic value and are
reported as a family, not guessed from that value.

Resuming a paused daemon currently requires stopping it, archiving its
`<state_file>.auto-act.json` record after investigation, and restarting it.
The independent hourly agent is not governed by the daemon brake; disable its
`agent_purge_expired` policy while suspending all automation. Shell-command
descendants and kernel-blocked calls are not guaranteed to stop at a deadline.

## Development validation (2026-09-26)

- Go 1.24.6: `./build.sh test` passed (formatting, vet, full ordinary suite).
- Race tests passed for mutation, worklimit, daemon, and plan packages.
- Cross-builds passed for darwin/linux on amd64/arm64.
- Linux amd64 `vmtest` binaries for plan and size compiled; not executed.
- VM driver shell syntax and result-summarizer pass/skip/fail fixture passed.
- No production cleanup, installation, guest provisioning, filesystem formatting,
  mount tests, or crash injection was performed on the development workstation.

These results establish source/build regression coverage, not filesystem release
certification. See the required release evidence above.
