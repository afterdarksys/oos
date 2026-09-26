package agent

import (
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const hostileExe = `/Users/a b/R&D <x> "q" 100%/$HOME\bin/oos`

func TestStableExecutablePrefersTheInvokedLink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "Cellar", "oos", "1.0", "bin", "oos")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bin", "oos")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got := stableExecutable(link, real); got != link {
		t.Errorf("the stable link must win over the versioned path: got %s", got)
	}
	t.Setenv("PATH", filepath.Dir(link))
	if got := stableExecutable("oos", real); got != link {
		t.Errorf("a bare name must resolve through PATH: got %s", got)
	}
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := stableExecutable(other, real); got != real {
		t.Errorf("a different file must not be installed: got %s", got)
	}
	if got := stableExecutable("./oos", real); got != real {
		t.Errorf("a relative argv[0] must fall back: got %s", got)
	}
}

func TestXMLTextAndSystemdQuote(t *testing.T) {
	var got string
	if err := xml.Unmarshal([]byte("<s>"+XMLText(hostileExe)+"</s>"), &got); err != nil || got != hostileExe {
		t.Fatalf("xml round trip: %q %v", got, err)
	}
	q := SystemdQuote(hostileExe)
	want := `"/Users/a b/R&D <x> \"q\" 100%%/$$HOME\\bin/oos"`
	if q != want {
		t.Fatalf("systemd quoting:\n got %s\nwant %s", q, want)
	}
}

func TestAgentFilesEscapeHostilePath(t *testing.T) {
	files := Files(t.TempDir(), hostileExe)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return
	}
	for p, c := range files {
		switch {
		case runtime.GOOS == "darwin":
			if !strings.Contains(c, XMLText(hostileExe)) {
				t.Errorf("plist must carry the escaped path: %s", c)
			}
			lintPlist(t, p, c)
		case strings.HasSuffix(p, ".service"):
			if !strings.Contains(c, "ExecStart="+SystemdQuote(hostileExe)+" --agent-tick") {
				t.Errorf("unit must quote the path: %s", c)
			}
		}
	}
}

// lintPlist checks well-formedness always and runs plutil -lint where it exists (macOS).
func lintPlist(t *testing.T, name, c string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(c))
	for {
		if _, err := d.Token(); err != nil {
			if err != io.EOF {
				t.Fatalf("plist is not well-formed XML: %v", err)
			}
			break
		}
	}
	pl, err := exec.LookPath("plutil")
	if err != nil {
		return
	}
	f := filepath.Join(t.TempDir(), filepath.Base(name))
	if err := os.WriteFile(f, []byte(c), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(pl, "-lint", f).CombinedOutput(); err != nil {
		t.Fatalf("plutil -lint: %v: %s", err, out)
	}
}
