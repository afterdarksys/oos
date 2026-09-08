// Package leftovers finds what applications left behind in ~/Library.
// Every entry under the areas apps write to is paired with an installed
// app by bundle identifier (com.vendor.app) or by name; an entry whose app
// is gone is an orphan. Labels and --add suggestions only: nothing is
// removed, and anything under com.apple is never judged.
package leftovers

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/size"
)

// App is an installed application bundle.
type App struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	BundleID string `json:"bundle_id,omitempty"`
}

// Areas are the ~/Library directories applications write into.
var Areas = []string{"Application Support", "Caches", "Containers", "Group Containers", "Saved Application State", "Logs", "WebKit", "HTTPStorages", "LaunchAgents", "Preferences"}

// AppDirs are searched for bundles. Tests override.
var AppDirs = []string{"/Applications", "/Applications/Utilities", "/System/Applications", "/System/Applications/Utilities", "/System/Library/CoreServices"}

// BundleID reads CFBundleIdentifier from an app's Info.plist. Tests stub it.
var BundleID = func(app string) string {
	out, err := exec.Command("plutil", "-extract", "CFBundleIdentifier", "raw", "-o", "-", filepath.Join(app, "Contents", "Info.plist")).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Row is one Library entry.
type Row struct {
	Path    string    `json:"path"`
	Area    string    `json:"area"`
	Name    string    `json:"name"`
	Bytes   int64     `json:"bytes"`
	ModTime time.Time `json:"mtime"`
	Verdict string    `json:"verdict"` // orphan | unmatched | installed | known
	App     string    `json:"app,omitempty"`
	Reason  string    `json:"reason,omitempty"`
}

// Result is the whole report.
type Result struct {
	Rows      []Row            `json:"rows"`
	Apps      int              `json:"apps_seen"`
	ByVerdict map[string]int64 `json:"bytes_by_verdict"`
	Elapsed   time.Duration
}

func norm(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return -1
	}, s)
}

// InstalledApps lists bundles under AppDirs and ~/Applications with their ids.
func InstalledApps(home string) []App {
	dirs := append([]string{}, AppDirs...)
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	var apps []App
	seen := map[string]bool{}
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !strings.HasSuffix(e.Name(), ".app") || seen[e.Name()] {
				continue
			}
			seen[e.Name()] = true
			p := filepath.Join(d, e.Name())
			apps = append(apps, App{Name: strings.TrimSuffix(e.Name(), ".app"), Path: p, BundleID: strings.ToLower(BundleID(p))})
		}
	}
	return apps
}

type index struct {
	ids   map[string]string // bundle id -> app name
	names map[string]string // normalized name -> app name
	last  map[string]string // last id component -> app name
}

func build(apps []App) index {
	ix := index{ids: map[string]string{}, names: map[string]string{}, last: map[string]string{}}
	for _, a := range apps {
		ix.names[norm(a.Name)] = a.Name
		if a.BundleID != "" {
			ix.ids[a.BundleID] = a.Name
			parts := strings.Split(a.BundleID, ".")
			ix.last[norm(parts[len(parts)-1])] = a.Name
		}
	}
	return ix
}

var idPrefixes = []string{"com.", "org.", "net.", "io.", "dev.", "app.", "co.", "me.", "de.", "at.", "uk.", "us.", "se.", "ch.", "fr.", "nl.", "jp.", "ru.", "sh.", "xyz.", "info.", "ai."}

func looksLikeID(name string) bool {
	l := strings.ToLower(name)
	for _, p := range idPrefixes {
		if strings.HasPrefix(l, p) && strings.Count(l, ".") >= 1 {
			return true
		}
	}
	return false
}

// judge pairs one entry name with the installed apps.
func (ix index) judge(name string) (verdict, app, reason string) {
	n := strings.TrimSuffix(strings.TrimSuffix(name, ".savedState"), ".plist")
	l := strings.ToLower(n)
	if strings.HasPrefix(l, "com.apple") || strings.HasPrefix(l, "group.com.apple") {
		return "installed", "macOS", "Apple"
	}
	l = strings.TrimPrefix(l, "group.")
	if looksLikeID(l) {
		if a, ok := ix.ids[l]; ok {
			return "installed", a, ""
		}
		// helpers and extensions: com.vendor.app.helper belongs to com.vendor.app
		for id, a := range ix.ids {
			if strings.HasPrefix(l, id+".") {
				return "installed", a, "helper of " + id
			}
		}
		parts := strings.Split(l, ".")
		if len(parts) >= 3 {
			if a, ok := ix.last[norm(parts[len(parts)-1])]; ok {
				return "installed", a, "matched by name"
			}
		}
		return "orphan", "", "no installed app has bundle id " + l
	}
	k := norm(n)
	if a, ok := ix.names[k]; ok {
		return "installed", a, ""
	}
	if a, ok := ix.last[k]; ok {
		return "installed", a, "matched by bundle id"
	}
	if len(k) >= 4 {
		for nk, a := range ix.names {
			if len(nk) >= 4 && (strings.Contains(nk, k) || strings.Contains(k, nk)) {
				return "installed", a, "matched loosely"
			}
		}
	}
	return "unmatched", "", "no installed app named " + n + " (may be a tool, not an app)"
}

// Scan reads every area under ~/Library and judges each entry over minBytes.
func Scan(cfg *config.Config, home string, apps []App, minBytes int64, f size.Filter, now time.Time) (*Result, error) {
	start := time.Now()
	lib := filepath.Join(home, "Library")
	if _, err := os.Stat(lib); err != nil {
		return nil, err
	}
	ix := build(apps)
	res := &Result{Apps: len(apps), ByVerdict: map[string]int64{}}
	for _, area := range Areas {
		dir := filepath.Join(lib, area)
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			fi, err := os.Lstat(p)
			if err != nil || fi.Mode()&os.ModeSymlink != 0 {
				continue
			}
			var bytes int64
			if fi.IsDir() {
				bytes, _ = size.PathSize(p)
			} else {
				bytes = size.Allocated(fi)
			}
			if bytes < minBytes || !f.KeepTime(fi.ModTime(), now) {
				continue
			}
			r := Row{Path: p, Area: area, Name: e.Name(), Bytes: bytes, ModTime: fi.ModTime()}
			r.Verdict, r.App, r.Reason = ix.judge(e.Name())
			for _, ent := range cfg.Entries(nil) {
				if config.IsUnder(p, ent.Path) || config.IsUnder(ent.Path, p) {
					r.Verdict, r.Reason = "known", ent.Action+" in oos.json"
					break
				}
			}
			if f.Tag != "" && f.Tag != r.Verdict {
				continue
			}
			res.Rows = append(res.Rows, r)
			res.ByVerdict[r.Verdict] += bytes
		}
	}
	rank := map[string]int{"orphan": 0, "unmatched": 1, "known": 2, "installed": 3}
	sort.Slice(res.Rows, func(i, j int) bool {
		if rank[res.Rows[i].Verdict] != rank[res.Rows[j].Verdict] {
			return rank[res.Rows[i].Verdict] < rank[res.Rows[j].Verdict]
		}
		return res.Rows[i].Bytes > res.Rows[j].Bytes
	})
	res.Elapsed = time.Since(start)
	return res, nil
}

// AddLine is the --add command that would register an orphan for cleanup.
func AddLine(r Row, home string) string {
	p := r.Path
	if config.IsUnder(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	action := config.ActionRmContents
	if fi, err := os.Lstat(r.Path); err == nil && !fi.IsDir() {
		action = config.ActionRm
	}
	return "oos --add \"" + p + "\" --type leftover --action " + action + " --use-case \"leftover: " + r.Name + "\" --tags leftover --note \"" + r.Reason + "\""
}
