// Package triage reads a downloads folder the way a person would: what is
// each thing, how old, and is there an obvious reason it can go. An
// installer whose app is already installed, an archive already extracted
// beside it, a "(2)" copy of a file that is still there, a half-finished
// download, an application bundle living in Downloads. Verdicts are labels;
// nothing is moved or removed.
package triage

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/afterdarksys/oos/internal/size"
)

const (
	StaleAfter = 180 * 24 * time.Hour
	bigBytes   = 1 << 30
)

// Row is one entry directly under the folder.
type Row struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Bytes    int64     `json:"bytes"`
	ModTime  time.Time `json:"mtime"`
	IsDir    bool      `json:"dir"`
	Type     string    `json:"type"`
	Verdicts []string  `json:"verdicts,omitempty"`
	Detail   string    `json:"detail,omitempty"` // the app, original or folder a verdict refers to
}

// Result is the whole report.
type Result struct {
	Root      string           `json:"root"`
	Rows      []Row            `json:"rows"`
	Total     int64            `json:"total_bytes"`
	ByVerdict map[string]int64 `json:"bytes_by_verdict"`
	Elapsed   time.Duration
}

// AppDirs are where installed applications are looked for. Tests override.
var AppDirs = []string{"/Applications", "/Applications/Utilities"}

var (
	partialExt = map[string]bool{"crdownload": true, "part": true, "download": true, "partial": true, "tmp": true, "aria2": true}
	copyName   = regexp.MustCompile(`^(.*?)(?: \(\d+\)| copy(?: \d+)?|-\d+)(\.[^.]+(?:\.[^.]+)?)?$`)
	copySuffix = regexp.MustCompile(`(?: \(\d+\)| copy(?: \d+)?)$`)
	separators = regexp.MustCompile(`[-_ .()+]+`)
	versionTok = regexp.MustCompile(`^v?\d+(?:[._]\d+)*(?:[a-z]\d*)?$`)
	noiseWords = map[string]bool{}
)

func init() {
	for _, w := range strings.Fields("x64 x86 x86_64 amd64 arm64 aarch64 universal mac macos osx darwin apple silicon intel installer setup latest stable release en us final") {
		noiseWords[w] = true
	}
}

// stem strips the extension, a copy suffix, and the version and platform
// tokens from an installer name so it can be matched against an app name.
func stem(name string) string {
	n := strings.ToLower(name)
	if e := size.ExtOf(n); e != "" {
		n = strings.TrimSuffix(n, "."+e)
	}
	n = copySuffix.ReplaceAllString(n, "")
	var keep []string
	for _, tok := range separators.Split(n, -1) {
		if tok == "" || noiseWords[tok] || versionTok.MatchString(tok) {
			continue
		}
		keep = append(keep, tok)
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.Join(keep, ""))
}

// installedApps lists app bundle names, normalized, under AppDirs.
func installedApps(home string) map[string]string {
	apps := map[string]string{}
	dirs := append([]string{}, AppDirs...)
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".app") {
				key := stem(strings.TrimSuffix(e.Name(), ".app"))
				if key != "" {
					apps[key] = filepath.Join(d, e.Name())
				}
			}
		}
	}
	return apps
}

// matchApp finds an installed app whose normalized name contains or is
// contained by the installer's stem, shortest distance first.
func matchApp(name string, apps map[string]string) string {
	s := stem(name)
	if len(s) < 3 {
		return ""
	}
	best, bestLen := "", 0
	for key, path := range apps {
		if len(key) < 3 {
			continue
		}
		if strings.Contains(key, s) || strings.Contains(s, key) {
			l := len(key)
			if strings.Contains(key, s) {
				l = len(key) - len(s) // closer to exact wins
			}
			if best == "" || l < bestLen {
				best, bestLen = path, l
			}
		}
	}
	return best
}

// Scan reads root one level deep and judges every entry.
func Scan(root, home string, f size.Filter, now time.Time) (*Result, error) {
	start := time.Now()
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	apps := installedApps(home)
	res := &Result{Root: root, ByVerdict: map[string]int64{}}
	names := map[string]bool{}
	sizes := map[string]int64{}
	var rows []Row
	for _, e := range ents {
		p := filepath.Join(root, e.Name())
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		names[e.Name()] = true
		r := Row{Path: p, Name: e.Name(), ModTime: fi.ModTime(), IsDir: fi.IsDir()}
		if r.IsDir {
			r.Bytes, _ = size.PathSize(p)
			r.Type = "folder"
			if strings.HasSuffix(r.Name, ".app") {
				r.Type = "application"
			}
		} else {
			r.Bytes = size.Allocated(fi)
			r.Type = size.TypeOf(p, r.Bytes)
		}
		sizes[r.Name] = r.Bytes
		rows = append(rows, r)
	}
	for _, r := range rows {
		if !f.KeepTime(r.ModTime, now) || !f.KeepName(r.Name) {
			continue
		}
		judge(&r, names, sizes, apps, now)
		res.Rows = append(res.Rows, r)
		res.Total += r.Bytes
		for _, v := range r.Verdicts {
			res.ByVerdict[v] += r.Bytes
		}
	}
	sort.Slice(res.Rows, func(i, j int) bool {
		if len(res.Rows[i].Verdicts) > 0 != (len(res.Rows[j].Verdicts) > 0) {
			return len(res.Rows[i].Verdicts) > 0
		}
		return res.Rows[i].Bytes > res.Rows[j].Bytes
	})
	res.Elapsed = time.Since(start)
	return res, nil
}

func judge(r *Row, names map[string]bool, sizes map[string]int64, apps map[string]string, now time.Time) {
	ext := size.ExtOf(r.Name)
	add := func(v string) { r.Verdicts = append(r.Verdicts, v) }
	switch {
	case r.Type == "application":
		add("app-in-downloads")
		r.Detail = "an application living here; move it to /Applications or remove it"
	case partialExt[ext] || (!r.IsDir && r.Bytes == 0):
		add("partial")
		r.Detail = "unfinished or empty download"
	}
	if r.Type == "disk image" || r.Type == "package" || ext == "dmg" || ext == "pkg" {
		if app := matchApp(r.Name, apps); app != "" {
			add("installed")
			r.Detail = "installer; " + app + " is installed"
		} else {
			add("installer")
		}
	}
	if r.Type == "archive" && !r.IsDir {
		base := strings.TrimSuffix(r.Name, "."+ext)
		if names[base] {
			add("extracted")
			r.Detail = "archive; " + base + "/ sits beside it"
		}
	}
	if m := copyName.FindStringSubmatch(r.Name); m != nil {
		orig := m[1] + m[2]
		if orig != r.Name && names[orig] {
			if sizes[orig] == r.Bytes {
				add("copy")
				r.Detail = "same size as " + orig
			} else {
				add("copy-differs")
				r.Detail = "named like a copy of " + orig + " but a different size"
			}
		}
	}
	if now.Sub(r.ModTime) >= StaleAfter {
		add("stale")
	}
	if r.Bytes >= bigBytes {
		add("big")
	}
}

// Print is the human report.
func Print(out *strings.Builder, res *Result, now time.Time, verbose bool, top int) {
	n := len(res.Rows)
	if top > 0 && top < n {
		n = top
	}
	flagged := 0
	for _, r := range res.Rows {
		if len(r.Verdicts) > 0 {
			flagged++
		}
	}
	fmt.Fprintf(out, "downloads under %s: %d entries, %s; %d flagged (%s):\n", res.Root, len(res.Rows), size.Human(res.Total), flagged, res.Elapsed.Round(100*time.Millisecond))
	for _, r := range res.Rows[:n] {
		v := strings.Join(r.Verdicts, ",")
		if v == "" {
			if !verbose {
				continue
			}
			v = "-"
		}
		fmt.Fprintf(out, "  %9s  %4dd  %-12s %-22s %s\n", size.Human(r.Bytes), int(now.Sub(r.ModTime).Hours()/24), r.Type, v, r.Name)
		if r.Detail != "" {
			fmt.Fprintf(out, "                                                     %s\n", r.Detail)
		}
	}
	keys := make([]string, 0, len(res.ByVerdict))
	for k := range res.ByVerdict {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return res.ByVerdict[keys[i]] > res.ByVerdict[keys[j]] })
	if len(keys) > 0 {
		fmt.Fprintln(out, "by verdict:")
		for _, k := range keys {
			fmt.Fprintf(out, "  %9s  %s\n", size.Human(res.ByVerdict[k]), k)
		}
	}
	fmt.Fprintln(out, "verdicts are labels; nothing was moved or removed")
}
