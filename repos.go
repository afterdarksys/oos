package main

// --scan-builds: where the bytes go inside source trees. Every git repository
// under a root is sized as source, .git and build output, with its last
// commit, dirty flag and newest source change, and the build directories
// that a clean, idle repo could lose without losing work are emitted as
// ready-to-paste --add lines. Read-only; the lines are suggestions and the
// guards (clean, idle for --older-than) are printed beside every refusal.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultRepoDepth   = 4
	defaultBuildIdle   = 30 * 24 * time.Hour
	defaultBuildFloor  = 50 << 20 // suggestions under this are noise (__pycache__, tiny dist)
	gitCommandTimeout  = 20 * time.Second
	maxSuggestionsText = 20
)

// BuildDir is one build-output directory inside a repo.
type BuildDir struct {
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
	Bytes       int64  `json:"bytes"`
	Suggest     bool   `json:"suggest"`
	Why         string `json:"why,omitempty"` // reason not suggested
}

// RepoRow is one repository.
type RepoRow struct {
	Path       string     `json:"path"`
	Name       string     `json:"name"`
	Source     int64      `json:"source_bytes"`
	Git        int64      `json:"git_bytes"`
	Build      int64      `json:"build_bytes"`
	Builds     []BuildDir `json:"builds,omitempty"`
	LastCommit time.Time  `json:"last_commit,omitempty"`
	Newest     time.Time  `json:"newest_source_mtime"`
	Dirty      bool       `json:"dirty"`
	DirtyKnown bool       `json:"dirty_known"` // false when git could not answer
	Nested     []string   `json:"nested_repos,omitempty"`
	Err        string     `json:"error,omitempty"`
}

// gitOut runs git in dir with a deadline. Tests replace it.
var gitOut = func(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], firstLine(msg))
	}
	return out, nil
}

// isRepo: a .git directory, or the .git file a worktree or submodule carries.
func isRepo(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// buildDirKind says whether name under parent is build output, by the
// fingerprint beside or inside it. "" means it is not.
func buildDirKind(parent, name string) (kind, fingerprint string) {
	beside := func(files ...string) string {
		for _, f := range files {
			if exists(filepath.Join(parent, f)) {
				return f
			}
		}
		return ""
	}
	full := filepath.Join(parent, name)
	switch name {
	case "target":
		if f := beside("Cargo.toml"); f != "" {
			return "Rust target", f + " beside"
		}
		if f := beside("pom.xml"); f != "" {
			return "Maven target", f + " beside"
		}
	case "node_modules":
		if f := beside("package.json"); f != "" {
			return "node_modules", f + " beside"
		}
	case ".next", ".nuxt", ".svelte-kit", ".parcel-cache", ".turbo", ".angular", ".vite":
		if f := beside("package.json"); f != "" {
			return name + " cache", f + " beside"
		}
	case "dist", "build", "out", "coverage", ".output", "storybook-static":
		if f := beside("package.json", "CMakeLists.txt", "build.gradle", "build.gradle.kts", "setup.py", "pyproject.toml", "Makefile", "meson.build", "BUILD", "WORKSPACE"); f != "" {
			return name + " output", f + " beside"
		}
	case ".venv", "venv", "env", ".env", "virtualenv":
		if exists(filepath.Join(full, "pyvenv.cfg")) {
			return "Python venv", "pyvenv.cfg inside"
		}
	case ".tox", ".nox", ".mypy_cache", ".pytest_cache", ".ruff_cache", "__pycache__", ".hypothesis":
		return "Python cache", name
	case ".terraform":
		return "Terraform providers", name
	case ".gradle":
		if f := beside("build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"); f != "" {
			return "Gradle cache", f + " beside"
		}
	case "zig-cache", "zig-out", ".zig-cache":
		if f := beside("build.zig"); f != "" {
			return "Zig build", f + " beside"
		}
	case ".dart_tool":
		if f := beside("pubspec.yaml"); f != "" {
			return "Dart tool cache", f + " beside"
		}
	case "Pods":
		if f := beside("Podfile"); f != "" {
			return "CocoaPods", f + " beside"
		}
	case "DerivedData", ".build":
		if f := beside("Package.swift", "Podfile"); f != "" || name == "DerivedData" {
			return "Xcode/SwiftPM build", name
		}
	case "bin", "obj":
		if m, _ := filepath.Glob(filepath.Join(parent, "*.csproj")); len(m) > 0 {
			return ".NET build", filepath.Base(m[0]) + " beside"
		}
	case "_build", "deps":
		if f := beside("mix.exs", "rebar.config"); f != "" {
			return "Elixir/Erlang build", f + " beside"
		}
	case ".cache":
		return "cache", "inside repo"
	}
	return "", ""
}

// findRepos lists every git repository under root up to depth levels down,
// skipping build output and other repos' insides only after recording them.
func findRepos(root string, depth int) ([]string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	rootDev, haveDev := deviceOf(info)
	var repos []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != root {
			if fi, err := d.Info(); err == nil && haveDev {
				if dev, ok := deviceOf(fi); ok && dev != rootDev {
					return fs.SkipDir
				}
			}
			name := d.Name()
			if name == ".git" {
				return fs.SkipDir
			}
			if k, _ := buildDirKind(filepath.Dir(p), name); k != "" {
				return fs.SkipDir
			}
			if strings.HasPrefix(name, ".") && name != ".config" {
				return fs.SkipDir
			}
		}
		if isRepo(p) {
			repos = append(repos, p)
		}
		if strings.Count(strings.TrimPrefix(p, root), string(filepath.Separator)) >= depth {
			return fs.SkipDir
		}
		return nil
	})
	sort.Strings(repos)
	return repos, err
}

// scanRepo sizes one repository. Nested repositories and build directories
// are not descended: the former are listed and scanned on their own, the
// latter sized through the cache.
func scanRepo(path string) RepoRow {
	r := RepoRow{Path: path, Name: filepath.Base(path)}
	info, err := os.Lstat(path)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	rootDev, haveDev := deviceOf(info)
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if p == path {
			return nil
		}
		if d.IsDir() {
			if haveDev {
				if dev, ok := deviceOf(fi); ok && dev != rootDev {
					return fs.SkipDir
				}
			}
			name := d.Name()
			if name == ".git" {
				r.Git, _ = pathSize(p)
				return fs.SkipDir
			}
			if isRepo(p) {
				r.Nested = append(r.Nested, p)
				return fs.SkipDir
			}
			if kind, fp := buildDirKind(filepath.Dir(p), name); kind != "" {
				b, _ := pathSize(p)
				r.Builds = append(r.Builds, BuildDir{Path: p, Kind: kind, Fingerprint: fp, Bytes: b})
				r.Build += b
				return fs.SkipDir
			}
		}
		if name := d.Name(); name == ".git" && !d.IsDir() {
			// worktree/submodule pointer file: the store lives elsewhere
			return nil
		}
		r.Source += allocated(fi)
		if fi.Mode().IsRegular() && fi.ModTime().After(r.Newest) {
			r.Newest = fi.ModTime()
		}
		return nil
	})
	sort.Slice(r.Builds, func(i, j int) bool { return r.Builds[i].Bytes > r.Builds[j].Bytes })
	if out, err := gitOut(path, "log", "-1", "--format=%ct"); err == nil {
		if secs, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
			r.LastCommit = time.Unix(secs, 0)
		}
	}
	if out, err := gitOut(path, "status", "--porcelain", "--untracked-files=normal"); err == nil {
		r.DirtyKnown = true
		r.Dirty = len(bytes.TrimSpace(out)) > 0
	} else if r.Err == "" {
		r.Err = err.Error()
	}
	return r
}

// judgeBuilds decides which build directories to suggest: the repo must be
// clean by git's word and idle (no source change and no commit) for at least
// idle. Anything else keeps its bytes and says why.
func judgeBuilds(r *RepoRow, idle time.Duration, floor int64, now time.Time) {
	for i := range r.Builds {
		b := &r.Builds[i]
		switch {
		case b.Bytes == 0:
			b.Why = "empty"
		case b.Bytes < floor:
			b.Why = "under " + human(floor)
		case !r.DirtyKnown:
			b.Why = "git could not say whether the repo is clean"
		case r.Dirty:
			b.Why = "repo has uncommitted changes"
		case now.Sub(r.Newest) < idle:
			b.Why = fmt.Sprintf("source changed %s ago", ageString(now.Sub(r.Newest)))
		case !r.LastCommit.IsZero() && now.Sub(r.LastCommit) < idle:
			b.Why = fmt.Sprintf("committed %s ago", ageString(now.Sub(r.LastCommit)))
		default:
			b.Suggest = true
		}
	}
}

// addLine is the --add command that would register a build directory.
func addLine(r RepoRow, b BuildDir, home string) string {
	p := b.Path
	if isUnder(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	return fmt.Sprintf("oos --add %q --type build --action rm-contents --use-case %q --tags build-output,repo:%s --note %q",
		p, r.Name+" "+b.Kind, r.Name, b.Fingerprint+"; rebuilds from source")
}

func doScanBuilds(cfg *Config, env Env, o *opts, now time.Time, out, errw io.Writer) int {
	root := expandHome(o.scanBuilds, env.Home)
	f, err := filterFromOpts(o)
	if err != nil {
		fmt.Fprintln(errw, "oos:", err)
		return exitUsage
	}
	idle := f.olderThan
	if idle == 0 {
		idle = defaultBuildIdle
	}
	depth := o.depth
	if depth <= 0 {
		depth = defaultRepoDepth
	}
	floor := int64(defaultBuildFloor)
	if o.minMB > 0 {
		floor = o.minMB << 20
	}
	repos, err := findRepos(root, depth)
	if err != nil {
		fmt.Fprintf(errw, "oos: scan-builds %s: %v\n", root, err)
		return exitUsage
	}
	rows := make([]RepoRow, len(repos))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, p := range repos {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i] = scanRepo(p)
			judgeBuilds(&rows[i], idle, floor, now)
		}(i, p)
	}
	wg.Wait()
	if f.newerThan > 0 {
		kept := rows[:0]
		for _, r := range rows {
			if now.Sub(r.Newest) <= f.newerThan {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	sort.SliceStable(rows, lessFor(f.sortBy,
		func(i int) int64 { return rows[i].Build },
		func(i int) time.Time { return rows[i].Newest },
		func(i int) string { return rows[i].Path }))
	shown := rows[:capRows(len(rows), f.top, 0)]
	if o.jsonOut {
		_ = json.NewEncoder(out).Encode(map[string]any{"root": root, "idle": idle.String(), "repos": shown})
		return exitOK
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
	fmt.Fprintf(out, "repos under %s (%d found to depth %d; idle means no change or commit for %s):\n", root, len(rows), depth, ageString(idle))
	fmt.Fprintf(out, "  %9s  %9s  %9s  %-7s %-6s %-6s %s\n", "build", ".git", "source", "commit", "edit", "state", "repo")
	for _, r := range shown {
		commit := "   -"
		if !r.LastCommit.IsZero() {
			commit = ageString(now.Sub(r.LastCommit))
		}
		edit := "   -"
		if !r.Newest.IsZero() {
			edit = ageString(now.Sub(r.Newest))
		}
		state := "clean"
		switch {
		case !r.DirtyKnown:
			state = "?"
		case r.Dirty:
			state = "dirty"
		}
		fmt.Fprintf(out, "  %9s  %9s  %9s  %-7s %-6s %-6s %s\n", human(r.Build), human(r.Git), human(r.Source), commit, edit, state, r.Path)
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
				fmt.Fprintf(out, "             %s %9s  %-22s %s  %s\n", mark, human(b.Bytes), b.Kind, filepath.Base(b.Path), why)
			}
		}
		if o.verbose && r.Err != "" {
			fmt.Fprintf(out, "             %s\n", r.Err)
		}
	}
	fmt.Fprintf(out, "  totals: build output %s, .git %s, source %s\n", human(build), human(git), human(src))
	fmt.Fprintf(out, "  %s in %d build dirs over %s of clean repos idle %s+ could be registered; --add lines below are suggestions, nothing was changed\n",
		human(suggestBytes), nSuggest, human(floor), ageString(idle))
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
	return exitOK
}
