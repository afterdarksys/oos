package guard

import (
	"github.com/afterdarksys/oos/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedFilesystemAliases(t *testing.T) {
	home := t.TempDir()
	for _, names := range [][2]string{{"Protected", "PROTECTED"}, {"caf\u00e9", "cafe\u0301"}} {
		t.Run(names[1], func(t *testing.T) {
			base := filepath.Join(home, names[0])
			alias := filepath.Join(home, names[1])
			if err := os.Mkdir(base, 0700); err != nil {
				t.Fatal(err)
			}
			a, err := os.Stat(alias)
			if os.IsNotExist(err) {
				t.Skip("filesystem distinguishes these spellings")
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := os.Stat(base)
			if !os.SameFile(a, b) {
				t.Fatal("fixture alias differs")
			}
			p := config.Policy{NeverTouch: []string{base}}
			if CheckRemovalPath(p, alias, home) == nil {
				t.Fatal("protected alias accepted")
			}
			child := filepath.Join(alias, "child")
			if err := os.Mkdir(child, 0700); err != nil {
				t.Fatal(err)
			}
			if CheckRemovalPath(p, child, home) == nil {
				t.Fatal("protected alias descendant accepted")
			}
		})
	}
}
