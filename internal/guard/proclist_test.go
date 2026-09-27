package guard

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunLinesExitAndTimeoutSemantics(t *testing.T) {
	var got []string
	collect := func(l string) { got = append(got, l) }
	if err := runLines("sh", []string{"-c", "printf 'a\\nb c\\n'"}, false, collect); err != nil || len(got) != 2 || got[1] != "b c" {
		t.Fatalf("lines: %q %v", got, err)
	}
	if err := runLines("sh", []string{"-c", "exit 1"}, true, collect); err != nil {
		t.Errorf("lsof-style exit 1 with nothing on stderr means nothing found: %v", err)
	}
	if err := runLines("sh", []string{"-c", "exit 1"}, false, collect); err == nil {
		t.Error("exit 1 must fail when not allowed")
	}
	if err := runLines("sh", []string{"-c", "echo lsof: WARNING: bad >&2; exit 1"}, true, collect); err == nil || !strings.Contains(err.Error(), "WARNING") {
		t.Errorf("exit 1 with stderr must fail and say why: %v", err)
	}
	if err := runLines("sh", []string{"-c", "echo x; exit 2"}, true, collect); err == nil {
		t.Error("exit 2 must fail")
	}
	if err := runLines("oos-no-such-binary", nil, true, collect); err == nil {
		t.Error("a missing binary must fail")
	}
	long := 0
	if err := runLines("sh", []string{"-c", "head -c 2000000 /dev/zero | tr '\\0' a; echo"}, false, func(l string) { long = len(l) }); err != nil || long != 2000000 {
		t.Errorf("long line: len=%d %v", long, err)
	}
}

func TestRunLinesTimesOut(t *testing.T) {
	old := CommandTimeout
	CommandTimeout = 200 * time.Millisecond
	t.Cleanup(func() { CommandTimeout = old })
	start := time.Now()
	err := runLines("sh", []string{"-c", "echo partial; sleep 20"}, true, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("a hung listing must fail closed: %v", err)
	}
	// A leftover child holding stdout must not keep us waiting either.
	err = runLines("sh", []string{"-c", "sleep 5 & echo started"}, true, func(string) {})
	if err == nil {
		t.Error("output pipe held open past exit must be an error")
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Errorf("timeouts took %s", el)
	}
}

func TestDecodeLsofName(t *testing.T) {
	for in, want := range map[string]string{
		`/a/plain`:       "/a/plain",
		`/a/b\x20c`:      "/a/b c",
		`/a/new\nline`:   "/a/new\nline",
		`/a/tab\tx`:      "/a/tab\tx",
		`/a/bad\xZZ`:     `/a/bad\xZZ`,
		`/a/trailing\`:   `/a/trailing\`,
		`/a/short\x4`:    `/a/short\x4`,
		`/caf\xc3\xa9/x`: "/café/x",
	} {
		if got := decodeLsofName(in); got != want {
			t.Errorf("decode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReferencedBoundariesAndForms(t *testing.T) {
	refs := []string{
		"java -cp /opt/a/lib.jar,/x/cache/one,/y -jar z",
		"sh -c cd /x/cache/two;make",
		"env\t/x/cache/three\tfoo",
		"(/x/cache/four)",
		"line\n/x/cache/five\nnext",
		`/x/cache/esc\x20aped/f`,
		"/x/cache//six/../six/f",
	}
	for _, c := range []string{"/x/cache/one", "/x/cache/two", "/x/cache/three", "/x/cache/four", "/x/cache/five", "/x/cache/esc aped", "/x/cache/six", "/x/cache/six/"} {
		if !Referenced(c, refs) {
			t.Errorf("%s: reference missed", c)
		}
	}
	for _, c := range []string{"/x/cache/on", "/x/cache/fiv", "/x/cache/sixx", ""} {
		if Referenced(c, refs) {
			t.Errorf("%q: must not match", c)
		}
	}
}

func TestReferencedDarwinAliases(t *testing.T) {
	oldP, oldC := refFoldPrivate, refFoldCase
	refFoldPrivate, refFoldCase = true, true
	t.Cleanup(func() { refFoldPrivate, refFoldCase = oldP, oldC })
	refs := []string{
		"/private/var/folders/ab/T/tool/run.sock",
		"python /tmp/Build/x.py",
		"/private/etc/foo.conf",
		"/private/varnish/cache/a",
	}
	for _, c := range []string{
		"/var/folders/ab/T/tool", // lsof reports the /private form
		"/private/tmp/build",     // argv uses the short form, other case
		"/etc/foo.conf",
		"/private/varnish/cache", // not an alias: raw form still matches
	} {
		if !Referenced(c, refs) {
			t.Errorf("%s: alias reference missed", c)
		}
	}
	for in, want := range map[string]string{
		"/private/var/x":                   "/var/x",
		"cd /private/tmp;ls":               "cd /tmp;ls",
		"/private/etc":                     "/etc",
		"/private/varnish/x":               "/private/varnish/x",
		"a=/private/var,b=/private/tmpfoo": "a=/var,b=/private/tmpfoo",
	} {
		if got := unprivate(in); got != want {
			t.Errorf("unprivate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReferencedNoFoldingOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("folding is on for darwin")
	}
	if Referenced("/x/Cache", []string{"/x/cache/f"}) {
		t.Error("case must matter where the filesystem is case-sensitive")
	}
}

func TestReferencesPropagatesTypedErrors(t *testing.T) {
	env := Env{
		Procs: func() ([]string, error) { return nil, nil },
		Cwds:  func() ([]string, error) { return nil, ErrPIDNamespace },
	}
	if _, err := env.References(); !errors.Is(err, ErrPIDNamespace) {
		t.Errorf("namespace error must stay recognisable: %v", err)
	}
	env.Cwds = nil
	env.Open = func() ([]string, error) { return nil, &UnreadableProcessesError{What: "open files", Count: 3, Seen: 9} }
	_, err := env.References()
	var ue *UnreadableProcessesError
	if !errors.As(err, &ue) || ue.Count != 3 || !strings.Contains(err.Error(), "3 of 9") {
		t.Errorf("unreadable error must stay recognisable: %v", err)
	}
}
