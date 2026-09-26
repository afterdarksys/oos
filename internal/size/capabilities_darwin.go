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
	var name []byte
	for _, v := range st.Fstypename {
		if v == 0 {
			break
		}
		name = append(name, byte(v))
	}
	c := Capabilities{Filesystem: string(name), Identity: fmt.Sprintf("%x", st.Fsid), ReadOnly: st.Flags&1 != 0, ExtentProbe: "unsupported", Sharing: "hardlinks"}
	if c.Filesystem == "apfs" {
		c.Sharing = "clones and snapshots; exclusive allocation unknown"
		c.Notes = []string{"First-block locations cannot establish whole-file sharing.", "Container free capacity may be shared with other volumes."}
	}
	if c.Filesystem == "hfs" {
		c.Notes = []string{"Case and Unicode aliases are compared by object identity."}
	}
	return c, nil
}
