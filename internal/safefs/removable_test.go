package safefs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckRemovableAcceptsOwnedReadOnlyTree(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "batch", "ro")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, "f"), []byte("x"), 0o444)
	os.Chmod(d, 0o555)
	defer os.Chmod(d, 0o755)
	if err := CheckRemovable(context.Background(), filepath.Join(root, "batch")); err != nil {
		t.Fatalf("owned read-only tree refused: %v", err)
	}
}
