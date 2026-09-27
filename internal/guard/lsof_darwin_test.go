//go:build darwin

package guard

import (
	"os"
	"path/filepath"
	"testing"
)

// The lsof listers (-b -w, streamed, bounded) still see a file this test
// holds open and its working directory. t.TempDir is under /var, which lsof
// reports as /private/var: Referenced must fold the alias.
func TestLsofListersSeeOwnFiles(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "held"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	files, err := ListOpenFiles()
	if err != nil || !Referenced(dir, files) {
		t.Fatalf("open file under %s not found (%d files): %v", dir, len(files), err)
	}
	byProc, err := OpenFilesByProcess()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range byProc {
		found = found || (o.PID == os.Getpid() && Referenced(dir, []string{o.Path}))
	}
	if !found {
		t.Error("OpenFilesByProcess missed our own open file")
	}
	wd, _ := os.Getwd()
	cwds, err := ListProcessCwds()
	if err != nil || !Referenced(wd, cwds) {
		t.Errorf("own cwd %s not listed: %v", wd, err)
	}
	procs, err := ListProcesses()
	if err != nil || len(procs) == 0 {
		t.Errorf("ps listing: %d %v", len(procs), err)
	}
}
