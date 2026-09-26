package size

import (
	"fmt"
	"syscall"
)

func Filesystem(path string) (Capabilities, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Capabilities{}, err
	}
	c := Capabilities{Filesystem: fmt.Sprintf("linux:0x%x", st.Type), Identity: fmt.Sprintf("%x", st.Fsid), ReadOnly: st.Flags&1 != 0, ExtentProbe: "unsupported", Sharing: "unknown"}
	switch uint64(st.Type) {
	case 0xef53:
		c.Filesystem = "ext2/3/4"
		c.ExtentProbe = "fiemap-if-supported"
		c.Sharing = "hardlinks; external snapshots unknown"
	case 0x58465342:
		c.Filesystem = "xfs"
		c.ExtentProbe = "fiemap-if-supported"
		c.Sharing = "hardlinks and optional reflinks"
	case 0x9123683e:
		c.Filesystem = "btrfs"
		c.ExtentProbe = "fiemap-if-supported"
		c.Sharing = "reflinks, snapshots, compressed extents"
		c.Notes = []string{"Data and metadata allocation are separate; subvolumes share filesystem capacity."}
	case 0x01021994:
		c.Filesystem = "tmpfs"
		c.Sharing = "hardlinks"
	}
	return c, nil
}
