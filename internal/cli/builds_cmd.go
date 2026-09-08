package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/repos"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/status"
)

const maxSuggestionsText = 20

// addLine is the --add command that would register a build directory.
func addLine(r repos.Row, b repos.BuildDir, home string) string {
	p := b.Path
	if config.IsUnder(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	return fmt.Sprintf("oos --add %q --type build --action rm-contents --use-case %q --tags build-output,repo:%s --note %q",
		p, r.Name+" "+b.Kind, r.Name, b.Fingerprint+"; rebuilds from source")
}

func doScanBuilds(cfg *config.Config, env guard.Env, o *opts, now time.Time, out, errw io.Writer) int {
	root := config.ExpandHome(o.scanBuilds, env.Home)
	f, err := filterFromOpts(o)
	if err != nil {
		fmt.Fprintln(errw, "oos:", err)
		return status.ExitUsage
	}
	idle := f.OlderThan
	if idle == 0 {
		idle = repos.DefaultIdle
	}
	depth := o.depth
	if depth <= 0 {
		depth = repos.DefaultDepth
	}
	floor := int64(repos.DefaultFloor)
	if o.minMB > 0 {
		floor = o.minMB << 20
	}
	found, err := repos.Find(root, depth)
	if err != nil {
		fmt.Fprintf(errw, "oos: scan-builds %s: %v\n", root, err)
		return status.ExitUsage
	}
	rows := make([]repos.Row, len(found))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, p := range found {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i] = repos.Scan(p)
			repos.Judge(&rows[i], idle, floor, now)
		}(i, p)
	}
	wg.Wait()
	if f.NewerThan > 0 {
		kept := rows[:0]
		for _, r := range rows {
			if now.Sub(r.Newest) <= f.NewerThan {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	sort.SliceStable(rows, size.LessFor(f.SortBy,
		func(i int) int64 { return rows[i].Build },
		func(i int) time.Time { return rows[i].Newest },
		func(i int) string { return rows[i].Path }))
	shown := rows[:size.CapRows(len(rows), f.Top, 0)]
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"root": root, "idle": idle.String(), "repos": shown})
		return status.ExitOK
	}
	var src, git, build, suggestBytes int64
	var nSuggest int
	var lines []string
	for _, r := range rows {
		src += r.Source
		git += r.Git
		build += r.Build
		for _, b := range r.Builds {
			if b.Suggest {
				nSuggest++
				suggestBytes += b.Bytes
				lines = append(lines, addLine(r, b, env.Home))
			}
		}
	}
	fmt.Fprintf(out, "repos under %s (%d found to depth %d; idle means no change or commit for %s):\n", root, len(rows), depth, size.AgeString(idle))
	fmt.Fprintf(out, "  %9s  %9s  %9s  %-7s %-6s %-6s %s\n", "build", ".git", "source", "commit", "edit", "state", "repo")
	for _, r := range shown {
		commit := "   -"
		if !r.LastCommit.IsZero() {
			commit = size.AgeString(now.Sub(r.LastCommit))
		}
		edit := "   -"
		if !r.Newest.IsZero() {
			edit = size.AgeString(now.Sub(r.Newest))
		}
		state := "clean"
		switch {
		case !r.DirtyKnown:
			state = "?"
		case r.Dirty:
			state = "dirty"
		}
		fmt.Fprintf(out, "  %9s  %9s  %9s  %-7s %-6s %-6s %s\n", size.Human(r.Build), size.Human(r.Git), size.Human(r.Source), commit, edit, state, r.Path)
		if o.verbose || r.Build > 0 {
			for _, b := range r.Builds {
				mark := "keep   "
				why := b.Why
				if b.Suggest {
					mark = "suggest"
					why = ""
				}
				if !o.verbose && !b.Suggest && b.Bytes < floor {
					continue
				}
				fmt.Fprintf(out, "             %s %9s  %-22s %s  %s\n", mark, size.Human(b.Bytes), b.Kind, filepath.Base(b.Path), why)
			}
		}
		if o.verbose && r.Err != "" {
			fmt.Fprintf(out, "             %s\n", r.Err)
		}
	}
	fmt.Fprintf(out, "  totals: build output %s, .git %s, source %s\n", size.Human(build), size.Human(git), size.Human(src))
	fmt.Fprintf(out, "  %s in %d build dirs over %s of clean repos idle %s+ could be registered; --add lines below are suggestions, nothing was changed\n",
		size.Human(suggestBytes), nSuggest, size.Human(floor), size.AgeString(idle))
	limit := len(lines)
	if !o.verbose && limit > maxSuggestionsText {
		limit = maxSuggestionsText
	}
	for _, l := range lines[:limit] {
		fmt.Fprintf(out, "    %s\n", l)
	}
	if rest := len(lines) - limit; rest > 0 {
		fmt.Fprintf(out, "    (+%d more; -v lists all, -j has them as data)\n", rest)
	}
	return status.ExitOK
}
