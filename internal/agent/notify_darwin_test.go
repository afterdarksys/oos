//go:build darwin

package agent

import (
	"strings"
	"testing"
)

func TestNotifyArgsPassUntrustedTextVerbatim(t *testing.T) {
	title := `oos: "disk" \ CRITICAL`
	msg := `/Users/x/a" & do shell script "touch /tmp/pwned" & "\n\\`
	a := notifyArgs(title, msg)
	n := len(a)
	if n < 3 || a[n-3] != "--" || a[n-2] != msg || a[n-1] != title {
		t.Fatalf("title and message must be the last argv entries verbatim: %q", a)
	}
	for _, s := range a[:n-3] {
		if strings.Contains(s, "pwned") || strings.Contains(s, "CRITICAL") {
			t.Fatalf("untrusted text reached the script source: %q", s)
		}
	}
}
