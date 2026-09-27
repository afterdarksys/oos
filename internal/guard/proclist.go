package guard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ErrPIDNamespace means oos is not in the initial PID namespace (a
// container, or a host it cannot verify), so the processes it can see are
// not every process that may use a path. Reference answers from such a view
// cannot be trusted; callers refuse reference-dependent deletes.
var ErrPIDNamespace = errors.New("cannot verify references: not running in the host's initial PID namespace")

// UnreadableProcessesError means some live processes could not be inspected,
// so any of them might be using the path. Count of Seen were unreadable.
type UnreadableProcessesError struct {
	What  string // what was being read: "open files", "working directories", ...
	Count int
	Seen  int
}

func (e *UnreadableProcessesError) Error() string {
	return fmt.Sprintf("cannot read %s of %d of %d running processes; they may be using the path", e.What, e.Count, e.Seen)
}

var otherUsersNotInspected atomic.Int64

// OtherUserProcessesNotInspected is how many processes owned by other users
// the last Linux open-file or working-directory listing could not read.
// Without root those are off limits by design and do not fail the listing;
// callers should log it ("N other-user processes not inspected"). It is 0
// when running as root (any unreadable process is then an error) and on
// platforms that list through lsof.
func OtherUserProcessesNotInspected() int { return int(otherUsersNotInspected.Load()) }

// CommandTimeout bounds every external process listing (ps, lsof). A
// listing that does not finish in time is an error, never a partial answer.
var CommandTimeout = 30 * time.Second

// commandWaitDelay bounds how long a finished or killed command may keep its
// output pipe open through a leftover child.
const commandWaitDelay = 2 * time.Second

// maxLine is the longest single output line accepted; longer is an error.
const maxLine = 16 << 20

// runLines runs name with args under CommandTimeout and hands fn each stdout
// line as it is read. exit1Empty accepts exit status 1 when nothing was
// written to stderr: lsof uses it both for "nothing found" and for processes
// it could not inspect with warnings suppressed. Any other failure, a
// timeout, or an over-long line is an error; callers discard what fn saw.
func runLines(name string, args []string, exit1Empty bool, fn func(line string)) error {
	ctx, cancel := context.WithTimeout(context.Background(), CommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = commandWaitDelay
	stderr := &cappedBuffer{max: 64 << 10}
	cmd.Stderr = stderr
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pw.Close()
		done <- err
	}()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		fn(sc.Text())
	}
	scanErr := sc.Err()
	if scanErr != nil {
		cancel()
		pr.CloseWithError(scanErr) // unblock the copier so Wait returns
	}
	waitErr := <-done
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s did not finish within %s", name, CommandTimeout)
	}
	if scanErr != nil {
		return fmt.Errorf("read %s output: %w", name, scanErr)
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		var ee *exec.ExitError
		if exit1Empty && errors.As(waitErr, &ee) && ee.ExitCode() == 1 && msg == "" {
			return nil
		}
		if msg != "" {
			return fmt.Errorf("%s: %v: %s", name, waitErr, msg)
		}
		return fmt.Errorf("%s: %v", name, waitErr)
	}
	return nil
}

type cappedBuffer struct {
	b   []byte
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - len(c.b); room > 0 {
		if len(p) > room {
			c.b = append(c.b, p[:room]...)
		} else {
			c.b = append(c.b, p...)
		}
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return string(c.b) }

// decodeLsofName undoes lsof's escaping of unprintable name bytes (\xNN and
// the C escapes \n \t \r \b \f). A literal backslash sequence is ambiguous,
// so callers that match references keep both forms.
func decodeLsofName(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'x':
				if i+3 < len(s) {
					if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
						b.WriteByte(byte(v))
						i += 3
						continue
					}
				}
			case 'n', 't', 'r', 'b', 'f':
				b.WriteByte(map[byte]byte{'n': '\n', 't': '\t', 'r': '\r', 'b': '\b', 'f': '\f'}[s[i+1]])
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
