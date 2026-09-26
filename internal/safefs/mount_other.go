//go:build !linux

package safefs

import (
	"fmt"
	"github.com/afterdarksys/oos/internal/size"
	"os"
)

type mountIdentity struct{ device uint64 }

func identity(r *os.Root) (mountIdentity, error) {
	fi, err := r.Stat(".")
	if err != nil {
		return mountIdentity{}, err
	}
	dev, ok := size.DeviceOf(fi)
	if !ok {
		return mountIdentity{}, fmt.Errorf("device unavailable")
	}
	return mountIdentity{dev}, nil
}
