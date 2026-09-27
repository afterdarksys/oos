package plan

import "testing"

func TestMountForPoolMatching(t *testing.T) {
	info := `22 1 0:21 / / rw,relatime shared:1 - btrfs /dev/sda2 rw,subvol=/@
23 22 0:22 / /home rw,relatime shared:2 - btrfs /dev/sda2 rw,subvol=/@home
24 22 0:23 / /srv rw shared:3 - zfs tank/srv rw
25 22 0:24 / /data rw shared:4 - zfs tank/data rw
26 22 0:25 / /other rw shared:5 - zfs pool2/x rw
27 22 0:26 / /mnt/with\040space rw shared:6 - ext4 /dev/sdb1 rw
`
	h, ok := mountFor(info, "/home/u/.cache")
	r, _ := mountFor(info, "/var/tmp")
	if !ok || h.point != "/home" || poolOf(h) != poolOf(r) {
		t.Fatalf("btrfs subvolumes must share a pool: %+v %+v", h, r)
	}
	s, _ := mountFor(info, "/srv/a")
	d, _ := mountFor(info, "/data")
	o, _ := mountFor(info, "/other/b")
	if poolOf(s) != "tank" || poolOf(s) != poolOf(d) || poolOf(o) == poolOf(s) {
		t.Fatalf("zfs pools: %q %q %q", poolOf(s), poolOf(d), poolOf(o))
	}
	if m, _ := mountFor(info, "/mnt/with space/f"); m.point != "/mnt/with space" {
		t.Fatalf("escaped mount point: %+v", m)
	}
}
