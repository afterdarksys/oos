package safefs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestMoveNoReplace(t *testing.T) {
	home := t.TempDir()
	src, dst := filepath.Join(home, "src"), filepath.Join(home, "dst")
	os.WriteFile(src, []byte("source"), 0o600)
	os.WriteFile(dst, []byte("precious"), 0o600)
	if err := MoveNoReplace(src, dst); err == nil {
		t.Fatal("existing destination overwritten")
	}
	b, err := os.ReadFile(dst)
	if err != nil || string(b) != "precious" {
		t.Fatalf("destination changed: %s %v", b, err)
	}
	os.Remove(dst)
	if err := MoveNoReplace(src, dst); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(dst)
	if err != nil || string(b) != "source" {
		t.Fatalf("move failed: %s %v", b, err)
	}
}
func TestCancelledRemovalPreservesTree(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "valuable")
	os.WriteFile(p, []byte("data"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RemoveContext(ctx, p, nil); err == nil {
		t.Fatal("canceled removal accepted")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

func TestNoReplaceUnsupportedIsNamed(t *testing.T) {
	for _, e := range []error{unix.EINVAL, unix.ENOSYS, unix.ENOTSUP, unix.EOPNOTSUPP} {
		if !noReplaceUnsupported(e) {
			t.Errorf("%v not mapped", e)
		}
	}
	if noReplaceUnsupported(unix.EEXIST) {
		t.Error("EEXIST must stay a plain conflict")
	}
	err := fmt.Errorf("rename a -> b: %w (%w)", ErrNoReplaceUnsupported, unix.EINVAL)
	if !errors.Is(err, ErrNoReplaceUnsupported) || !strings.Contains(err.Error(), "quarantine unavailable on this volume") {
		t.Fatalf("message: %v", err)
	}
}
