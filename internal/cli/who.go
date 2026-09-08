package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/status"
)

// whoResult is the answer to "what is this path for and who is using it".
type whoResult struct {
	Path        string             `json:"path"`
	Bytes       int64              `json:"bytes"`
	UseCase     string             `json:"use_case,omitempty"`
	Source      string             `json:"use_case_source,omitempty"`
	Attribution config.Attribution `json:"attribution"`
	Processes   []string           `json:"processes"`
	ProcessN    int                `json:"process_count"`
	NewestFile  string             `json:"newest_file,omitempty"`
	NewestAt    time.Time          `json:"newest_at,omitempty"`
}

// processHead trims a command line to something a human can scan.
func processHead(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if len(cmd) > 100 {
		cmd = cmd[:100] + "..."
	}
	return cmd
}

func doWho(cfg *config.Config, env guard.Env, o *opts, now time.Time, out, errw io.Writer) int {
	p := config.ExpandHome(o.who, env.Home)
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	if !config.Exists(p) {
		fmt.Fprintf(errw, "oos: %s does not exist\n", p)
		return status.ExitUsage
	}
	r := whoResult{Path: p, Attribution: config.Attribute(p)}
	r.UseCase, r.Source = cfg.UseCaseFor(p)
	if !o.quick {
		r.Bytes, _ = size.PathSize(p)
	}
	if refs, err := env.References(); err == nil {
		seen := map[string]bool{}
		for _, ref := range refs {
			if guard.Referenced(p, []string{ref}) {
				r.ProcessN++
				h := processHead(ref)
				if !seen[h] && len(r.Processes) < 5 {
					seen[h] = true
					r.Processes = append(r.Processes, h)
				}
			}
		}
	}
	r.NewestFile, r.NewestAt = size.NewestFile(p, 50000)
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(r)
		return status.ExitOK
	}
	fmt.Fprintln(out, p)
	if !o.quick {
		fmt.Fprintf(out, "  size        %s\n", size.Human(r.Bytes))
	}
	if r.UseCase != "" {
		fmt.Fprintf(out, "  use case    %s  (from %s)\n", r.UseCase, r.Source)
	} else {
		fmt.Fprintln(out, "  use case    unknown; add an owners pattern or --add it with --use-case")
	}
	if r.Attribution.Repo != "" {
		fmt.Fprintf(out, "  repo        %s\n", r.Attribution.Repo)
	}
	if r.Attribution.Fingerprint != "" {
		fmt.Fprintf(out, "  fingerprint %s\n", r.Attribution.Fingerprint)
	}
	if r.ProcessN > 0 {
		fmt.Fprintf(out, "  processes   %d referencing it\n", r.ProcessN)
		for _, h := range r.Processes {
			fmt.Fprintf(out, "              %s\n", h)
		}
	} else {
		fmt.Fprintln(out, "  processes   none reference it")
	}
	if r.NewestFile != "" {
		fmt.Fprintf(out, "  newest      %s  (%s ago)\n", r.NewestFile, now.Sub(r.NewestAt).Round(time.Minute))
	}
	return status.ExitOK
}
