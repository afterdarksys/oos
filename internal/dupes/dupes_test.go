package dupes

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/testutil"
)

func put(t *testing.T, p string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindGroupsIdenticalContentOnly(t *testing.T) {
	home := t.TempDir()
	a := bytes.Repeat([]byte("A"), 2<<20)
	b := bytes.Repeat([]byte("B"), 2<<20)
	// same size, same head and tail, different middle: the full hash must split them
	c := append(append(append([]byte{}, a[:1<<20]...), []byte("xx")...), a[1<<20+2:]...)
	put(t, filepath.Join(home, "one", "iso-a.iso"), a)
	put(t, filepath.Join(home, "two", "iso-a (1).iso"), a)
	put(t, filepath.Join(home, "three", "copy.iso"), a)
	put(t, filepath.Join(home, "b.iso"), b)
	put(t, filepath.Join(home, "almost.iso"), c)
	put(t, filepath.Join(home, "small.txt"), []byte("tiny"))
	if err := os.Link(filepath.Join(home, "b.iso"), filepath.Join(home, "b-link.iso")); err != nil {
		t.Skip("hardlinks unsupported here")
	}
	testutil.Age(t, filepath.Join(home, "three", "copy.iso"), 48*time.Hour)
	now := time.Now()

	res, err := Find(home, 1<<20, size.Filter{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 1 {
		t.Fatalf("want one group of the three identical isos, got %d: %+v", len(res.Groups), res.Groups)
	}
	g := res.Groups[0]
	if len(g.Files) != 3 || g.Wasted != 2*(2<<20) || g.Bytes != 2<<20 {
		t.Errorf("group: %+v", g)
	}
	if filepath.Base(g.Files[len(g.Files)-1].Path) != "copy.iso" {
		t.Errorf("oldest copy last, got %s", g.Files[len(g.Files)-1].Path)
	}
	if res.Wasted != g.Wasted || res.Scanned != 6 {
		t.Errorf("result totals: wasted %d scanned %d", res.Wasted, res.Scanned)
	}
	// the hardlink pair must not be a group (same inode), and the near-miss must not either
	for _, g := range res.Groups {
		for _, f := range g.Files {
			if filepath.Base(f.Path) == "b.iso" || filepath.Base(f.Path) == "almost.iso" {
				t.Errorf("%s must not be in a group", f.Path)
			}
		}
	}

	// floor above the files: nothing scanned
	if r, _ := Find(home, 4<<20, size.Filter{}, now); r.Scanned != 0 || len(r.Groups) != 0 {
		t.Errorf("floor: %+v", r)
	}
	// age window keeps only the old copy: no pair left
	if r, _ := Find(home, 1<<20, size.Filter{OlderThan: 24 * time.Hour}, now); len(r.Groups) != 0 || r.Scanned != 1 {
		t.Errorf("older-than: %+v", r)
	}
	if _, err := Find(filepath.Join(home, "nope"), 0, size.Filter{}, now); err == nil {
		t.Error("missing root must fail")
	}
}
