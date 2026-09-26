package safefs

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/afterdarksys/oos/internal/size"
)

type mountIdentity struct {
	device uint64
	mount  string
}

// fdinfo's mount ID distinguishes bind mounts even when st_dev is identical.
func identity(r *os.Root) (mountIdentity, error) {
	f, err := r.Open(".")
	if err != nil {
		return mountIdentity{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return mountIdentity{}, err
	}
	dev, ok := size.DeviceOf(fi)
	if !ok {
		return mountIdentity{}, fmt.Errorf("device unavailable")
	}
	b, err := os.ReadFile("/proc/self/fdinfo/" + strconv.FormatUint(uint64(f.Fd()), 10))
	if err != nil {
		return mountIdentity{}, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "mnt_id:" {
			return mountIdentity{dev, fields[1]}, nil
		}
	}
	return mountIdentity{}, fmt.Errorf("mount ID unavailable for %s", r.Name())
}
