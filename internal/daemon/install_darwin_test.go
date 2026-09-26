//go:build darwin

package daemon

import (
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afterdarksys/oos/internal/agent"
)

func TestDaemonPlistEscapesAndRestartsOnlyOnFailure(t *testing.T) {
	exe := `/Users/a b/R&D <x> "q"/oos`
	for p, c := range Files(t.TempDir(), exe, false) {
		for _, want := range []string{
			"<string>" + agent.XMLText(exe) + "</string>",
			"<key>SuccessfulExit</key><false/>",
			"<key>ThrottleInterval</key><integer>60</integer>",
			"<key>ProcessType</key><string>Background</string>",
		} {
			if !strings.Contains(c, want) {
				t.Errorf("plist lacks %s:\n%s", want, c)
			}
		}
		d := xml.NewDecoder(strings.NewReader(c))
		for {
			if _, err := d.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("plist is not well-formed XML: %v", err)
			}
		}
		pl, err := exec.LookPath("plutil")
		if err != nil {
			continue
		}
		f := filepath.Join(t.TempDir(), filepath.Base(p))
		if err := os.WriteFile(f, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(pl, "-lint", f).CombinedOutput(); err != nil {
			t.Fatalf("plutil -lint: %v: %s", err, out)
		}
	}
}
