package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/afterdarksys/oos/internal/agent"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/mutation"
	"github.com/afterdarksys/oos/internal/plan"
	"github.com/afterdarksys/oos/internal/state"
	"github.com/afterdarksys/oos/internal/status"
)

// exitFor maps a mutation error to its exit code: a busy lock is 4
// (retryable, nothing was tried), a partial run is 5, anything else is
// fallback.
func exitFor(err error, fallback int) int {
	switch {
	case err == nil:
		return status.ExitOK
	case errors.Is(err, mutation.ErrBusy):
		return status.ExitBusy
	case errors.Is(err, plan.ErrPartial):
		return status.ExitPartial
	}
	return fallback
}

// kindRefused is the "error_kind" of a mutation that did nothing because
// everything it would have done was refused (exit 2).
const kindRefused = "refused"

// ranExit maps the error of a mutation that ran to its exit code and JSON
// "error_kind": busy 4, partial 5 (some items were done), an unwritable
// audit log or an unusable store lock 6, and anything else, given fallback
// ExitCritical, a refusal where nothing was done: 2 "refused". Other
// fallbacks keep their own kind.
func ranExit(err error, fallback int) (int, string) {
	switch {
	case err == nil:
		return status.ExitOK, ""
	case errors.Is(err, mutation.ErrBusy):
		return status.ExitBusy, status.Kind(status.ExitBusy)
	case errors.Is(err, plan.ErrPartial):
		return status.ExitPartial, status.Kind(status.ExitPartial)
	case errors.Is(err, plan.ErrAudit), errors.Is(err, plan.ErrStoreLock):
		return status.ExitIO, status.Kind(status.ExitIO)
	case fallback == status.ExitCritical:
		return status.ExitCritical, kindRefused
	}
	return fallback, status.Kind(fallback)
}

// planRefusedOnly reports whether a live cleanup had work entries and every
// one was refused by policy at plan time, so nothing could be executed
// (exit 7). Entries marked never are declarations, not work, and a missing
// path is nothing to do rather than a refusal; neither counts.
func planRefusedOnly(items []plan.Item) bool {
	n := 0
	for _, it := range items {
		if it.Action == config.ActionNever {
			continue
		}
		var r *guard.Refusal
		if it.Refused == nil || errors.As(it.Refused, &r) && r.Rule == "missing" {
			return false
		}
		n++
	}
	return n > 0
}

// nothingActionable is the message and exit of a live cleanup whose every
// entry was refused by policy.
const nothingActionable = "nothing actionable: every entry was refused by policy"

// otherUserProcesses is guard.OtherUserProcessesNotInspected; a variable
// for package tests.
var otherUserProcesses = guard.OtherUserProcessesNotInspected

// uninspectedNote is the plan/result line for processes of other users a
// non-root listing could not read; "" when there were none.
func uninspectedNote() string {
	if n := otherUserProcesses(); n > 0 {
		return fmt.Sprintf("note: %d processes of other users could not be inspected", n)
	}
	return ""
}

// setPath puts path into a JSON row, and path_b64 beside it when the path
// is not valid UTF-8 (encoding/json would silently replace those bytes, and
// the row would name a file that does not exist).
func setPath(m map[string]any, path string) map[string]any {
	m["path"] = path
	if !utf8.ValidString(path) {
		m["path_b64"] = base64.StdEncoding.EncodeToString([]byte(path))
	}
	return m
}

// jsonError is the one-line JSON document a -j run emits when it fails
// before it has anything else to say; the human message goes to stderr.
func jsonError(out io.Writer, kind string, code int, err error) int {
	_ = json.NewEncoder(out).Encode(map[string]any{"kind": kind, "error": err.Error(), "error_kind": status.Kind(code)})
	return code
}

// failf reports a failure: the message on stderr always, and in -j mode a
// JSON error document of the given kind on stdout (never text).
func failf(o *opts, out, errw io.Writer, kind string, code int, format string, a ...any) int {
	err := fmt.Errorf(format, a...)
	fmt.Fprintln(errw, "oos:", err)
	if o.jsonOut {
		return jsonError(out, kind, code, err)
	}
	return code
}

// stateFiles are the files oos saves atomically; their directories hold
// its crash leftovers.
func stateFiles(cfg *config.Config) []string {
	p := cfg.Policy
	return []string{p.StateFile, p.SizeCacheFile}
}

// sweepLeftovers removes crash leftovers (old own temp files, surplus
// .corrupt copies) at the start of a live run or the daemon.
func sweepLeftovers(cfg *config.Config, now time.Time, errw io.Writer, verbose bool) {
	removed := state.SweepLeftovers(stateFiles(cfg), now)
	if verbose && len(removed) > 0 {
		fmt.Fprintf(errw, "oos: removed %d crash leftovers from the state directory\n", len(removed))
	}
}

// commandEntries counts configured entries with action "command".
func commandEntries(cfg *config.Config) int {
	n := 0
	for _, e := range append(append([]config.Entry{}, cfg.KnownDirs...), cfg.KnownFiles...) {
		if e.Action == config.ActionCommand {
			n++
		}
	}
	return n
}

// warnSkippedCommands tells a host once (a marker beside the state file)
// that the command entries of its own config file do not run because
// allow_commands is false. The embedded default's entries never warn: src
// is config.Load's source, and anything but a file is not the host's
// choice.
func warnSkippedCommands(cfg *config.Config, src string, errw io.Writer) {
	if cfg.Policy.AllowCommands || cfg.Policy.StateFile == "" || strings.HasPrefix(src, "embedded default") {
		return
	}
	n := commandEntries(cfg)
	if n == 0 {
		return
	}
	marker := cfg.Policy.StateFile + ".allow-commands-warned"
	if _, err := os.Lstat(marker); err == nil {
		return
	}
	fmt.Fprintf(errw, "oos: %d command entries are skipped because allow_commands is false; set allow_commands: true to run them\n", n)
	_ = plan.WriteRecord(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"))
}

// configRejectedMarker records the last "config rejected" notification of
// the agent tick, beside the default state file (the real one is unknown).
func configRejectedMarker(home string) string {
	if cfg, err := config.Degraded(home); err == nil && cfg.Policy.StateFile != "" {
		return cfg.Policy.StateFile + ".config-rejected"
	}
	return filepath.Join(home, ".local", "state", "oos", "config-rejected")
}

// agentJitter sleeps the launchd agent's random pre-tick delay; false means
// the run was interrupted and should do nothing.
func agentJitter(ctx context.Context) bool {
	return agent.Sleep(ctx, agent.JitterDelay(os.Getenv, nil))
}

// withKind returns v as a JSON object with a top-level "kind" field. A
// value that is not an object (a list) is returned unchanged, keeping the
// shape existing readers parse.
func withKind(kind string, v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return v
	}
	m["kind"] = kind
	return m
}
