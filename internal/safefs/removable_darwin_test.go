package safefs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCheckRemovableRefusesImmutable(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "batch", "locked")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte("x"), 0o644)
	if err := unix.Chflags(f, unix.UF_IMMUTABLE); err != nil {
		t.Skip(err)
	}
	defer unix.Chflags(f, 0)
	err := CheckRemovable(context.Background(), filepath.Join(root, "batch"))
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("immutable file not reported: %v", err)
	}
}
