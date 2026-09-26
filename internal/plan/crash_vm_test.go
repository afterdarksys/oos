//go:build linux && vmtest

package plan

import (
	"github.com/afterdarksys/oos/internal/testutil"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func requireDisposableVM(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile("/etc/oos-disposable-vm")
	if os.Getenv("OOS_DISPOSABLE_VM") != "1" || err != nil || string(b) != "OOS-DISPOSABLE-VM-V1\n" {
		t.Skip("requires explicitly marked disposable Linux VM")
	}
}
func TestVMCrashHelper(t *testing.T) {
	requireDisposableVM(t)
	if os.Getenv("OOS_CRASH_HELPER") != "1" {
		return
	}
	src, dir := os.Getenv("OOS_CRASH_SOURCE"), os.Getenv("OOS_CRASH_STORE")
	q, err := OpenQuarantine(dir, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	q.checkpoint = func(stage string) error {
		if stage == os.Getenv("OOS_CRASH_STAGE") {
			os.Exit(73)
		}
		return nil
	}
	_, err = q.take(src, 4096, time.Now())
	if err != nil {
		t.Fatal(err)
	}
}
func TestVMCrashRecovery(t *testing.T) {
	requireDisposableVM(t)
	for _, stage := range []string{"intent-durable", "payload-moved", "move-durable", "complete-durable"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			src, store := filepath.Join(root, "source"), filepath.Join(root, "q")
			testutil.Write(t, src, 4096)
			c := exec.Command(os.Args[0], "-test.run=^TestVMCrashHelper$")
			c.Env = append(os.Environ(), "OOS_CRASH_HELPER=1", "OOS_CRASH_STAGE="+stage, "OOS_CRASH_SOURCE="+src, "OOS_CRASH_STORE="+store)
			err := c.Run()
			if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 73 {
				t.Fatalf("crash checkpoint not reached: %v", err)
			}
			bs, err := ListBatches(store)
			if err != nil || len(bs) != 1 {
				t.Fatalf("batch discovery: %v %v", bs, err)
			}
			if _, err := RecoverBatch(store, bs[0].Name, true); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(src); os.IsNotExist(err) {
				if _, _, err := RestoreBatch(store, bs[0].Name, nil); err != nil {
					t.Fatal(err)
				}
			}
			b, err := os.ReadFile(src)
			if err != nil || len(b) != 4096 {
				t.Fatalf("payload lost: %d %v", len(b), err)
			}
		})
	}
}
