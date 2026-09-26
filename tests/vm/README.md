# Disposable filesystem test environment

Do not run this harness on the primary workstation or attach any real disks,
shared host directories, credentials, or production data to the guest.

Use a disposable Linux VM (QEMU/UTM, another hypervisor, or CI VM), with a 16–24 GB
system disk, 2–4 virtual CPUs, and 4 GB RAM. A separate disk per filesystem is
optional: the harness creates four small regular image files in the guest and
mounts one at a time, which exercises real filesystem implementations without
accepting arbitrary block devices. The VM system disk must have at least 6 GB
free for images, dependencies, and build artifacts. Clone/snapshot the VM before
running anything and discard it after collecting results.

Inside the disposable guest, install Go 1.24+, Python 3, util-linux, e2fsprogs,
xfsprogs, and btrfs-progs. Copy or clone this repository into the guest. No host
filesystem needs to be shared. After independently reviewing `run-linux.sh`,
explicitly mark the guest:

```sh
# DISPOSABLE GUEST ONLY
printf 'OOS-DISPOSABLE-VM-V1\n' | sudo tee /etc/oos-disposable-vm
sudo env OOS_DISPOSABLE_VM=1 bash tests/vm/run-linux.sh
```

The driver checks Linux, root, the explicit environment opt-in, the guest marker,
and VM detection. It accepts no paths/devices. It creates 768 MiB regular image
files exclusively under a fresh guest temporary directory, formats those files,
mounts one at a time, and sets TMPDIR so test fixtures are on that filesystem.
The `vmtest` tag enables separate process-exit checkpoint and reflink/snapshot
fixtures. All tests run against fixtures, not guest system files. Bind-mount tests
also require the guest marker and must not be enabled on a workstation.

Artifacts remain in the printed guest directory: per-filesystem Go JSON logs,
formatting logs, environment details, and summaries with skipped tests listed.
A summary deliberately never sets `release_approved` to true. A passing baseline
matrix still requires the release gates in `docs/FILESYSTEM-RELEASE-REVIEW.md`.
Interrupted runs retain image files for investigation. The trap attempts to
unmount the active fixture; if a busy mount cannot detach, discard the VM.

For HFS+/APFS, use a disposable macOS environment and separate disk images. Do
not adapt this Linux formatter to physical Mac disks. The first macOS matrix
should be reviewed and run independently after the Linux baseline is stable.
