package size

import (
	"context"
	"github.com/afterdarksys/oos/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

func TestAccountingExternalHardlink(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "remove")
	file := filepath.Join(root, "link")
	testutil.Write(t, file, 4096)
	if err := os.Link(file, filepath.Join(home, "keep")); err != nil {
		t.Fatal(err)
	}
	a, err := Account(context.Background(), nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if a.ExternalHardlinks != 1 || a.Upper >= a.Allocated {
		t.Fatalf("outside link not retained: %+v", a)
	}
}
func TestAccountingSparseAndCancellation(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "sparse")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(16 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	a, err := Account(context.Background(), nil, p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Logical != 16<<20 || a.Allocated >= a.Logical {
		t.Fatalf("sparse accounting: %+v", a)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a, err = Account(ctx, nil, p)
	if err == nil || a.Complete {
		t.Fatal("cancellation lost")
	}
}
func TestFilesystemCapabilities(t *testing.T) {
	c, err := Filesystem(t.TempDir())
	if err != nil || c.Filesystem == "" || c.Identity == "" {
		t.Fatalf("capabilities %+v %v", c, err)
	}
}
