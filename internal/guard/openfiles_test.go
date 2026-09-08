package guard

import (
	"os"
	"testing"
)

func TestReferencesIncludeOpenFilesAndFailClosed(t *testing.T) {
	env := Env{
		Procs: func() ([]string, error) { return []string{"p"}, nil },
		Cwds:  func() ([]string, error) { return []string{"/c"}, nil },
		Open:  func() ([]string, error) { return []string{"/o/file"}, nil },
	}
	refs, err := env.References()
	if err != nil || len(refs) != 3 || !Referenced("/o", refs) {
		t.Errorf("open files must be part of references: %v %v", refs, err)
	}
	env.Open = func() ([]string, error) { return nil, os.ErrPermission }
	if _, err := env.References(); err == nil {
		t.Error("open-file listing failure must fail closed")
	}
	env.Open = nil
	if refs, err := env.References(); err != nil || len(refs) != 2 {
		t.Errorf("nil Open disables the source: %v %v", refs, err)
	}
}
