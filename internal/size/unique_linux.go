package size

// physOffset has no cheap portable answer on Linux (FIEMAP is per
// filesystem and reflinks are rare outside btrfs and xfs), so large files
// are keyed by inode like everything else: hardlinks dedupe, reflinks do not.
func physOffset(string) (uint64, bool) { return 0, false }
