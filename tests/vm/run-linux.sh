#!/usr/bin/env bash
# DESTRUCTIVE FIXTURE TESTS: disposable Linux VM only. Never run on a workstation.
# No caller-supplied block devices or paths are accepted. Only newly-created
# regular image files are formatted, and all mounts are temporary.
set -euo pipefail
[[ $# == 0 ]] || { echo "This script accepts no devices or path arguments" >&2; exit 2; }
[[ $(uname -s) == Linux && ${OOS_DISPOSABLE_VM:-0} == 1 && $(id -u) == 0 ]] || {
  echo "Requires root inside an explicitly opted-in disposable Linux VM" >&2; exit 2;
}
[[ -f /etc/oos-disposable-vm && $(cat /etc/oos-disposable-vm) == OOS-DISPOSABLE-VM-V1 ]] || {
  echo "Disposable guest marker missing" >&2; exit 2;
}
systemd-detect-virt --vm >/dev/null || { echo "VM isolation not detected; refusing" >&2; exit 2; }
for cmd in python3 go mkfs.ext3 mkfs.ext4 mkfs.xfs mkfs.btrfs mount umount truncate sha256sum; do
  command -v "$cmd" >/dev/null || { echo "Missing $cmd" >&2; exit 2; }
done
repo=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo"
work=$(mktemp -d /var/tmp/oos-vm-matrix.XXXXXXXX)
mountpoint=""
cleanup() { if [[ -n "$mountpoint" ]] && mountpoint -q "$mountpoint"; then umount "$mountpoint"; fi; }
trap cleanup EXIT INT TERM
printf 'Artifacts: %s\n' "$work"
go version > "$work/environment.txt"
uname -a >> "$work/environment.txt"
export GOCACHE="$work/go-cache" GOMODCACHE="$work/go-mod"
for fs in ext3 ext4 xfs btrfs; do
  image="$work/$fs.img"
  mountpoint="$work/$fs"
  mkdir "$mountpoint"
  (set -o noclobber; : > "$image")
  [[ -f "$image" && ! -L "$image" ]] || exit 2
  truncate -s 768M "$image"
  case "$fs" in
    ext3|ext4) "mkfs.$fs" -F "$image" > "$work/$fs.mkfs.log" 2>&1 ;;
    xfs|btrfs) "mkfs.$fs" -f "$image" > "$work/$fs.mkfs.log" 2>&1 ;;
  esac
  mount -o loop "$image" "$mountpoint"
  mkdir "$mountpoint/tmp"
  # All t.TempDir fixtures are on this disposable filesystem.
  TMPDIR="$mountpoint/tmp" \
    go test -count=1 -tags vmtest -json ./... > "$work/$fs.tests.json"
  # Any required feature skip is recorded for reviewers; it is not a pass.
  python3 tests/vm/summarize.py "$work/$fs.tests.json" > "$work/$fs.summary.json"
  umount "$mountpoint"
  mountpoint=""
done
printf 'Matrix completed. Preserve %s for Fable/Grok review.\n' "$work"
