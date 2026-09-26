package mutation

import (
	"testing"
)

func TestExclusiveMutationLock(t *testing.T) {
	home := t.TempDir()
	first, err := Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Acquire(home); err == nil {
		second.Close()
		t.Fatal("concurrent writer accepted")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}
