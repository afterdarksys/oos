package state

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LeftoverAge is how old an own-prefix temp file must be before a sweep
// removes it: no live writer keeps one that long.
const LeftoverAge = time.Hour

// corruptKeep is how many .corrupt-* copies of a state file are kept.
const corruptKeep = 3

// SweepLeftovers removes crash leftovers next to the files oos saves
// atomically (the state file, the size cache): temp files older than
// LeftoverAge named by oos itself (.<base>.tmp-*, .oos-record-*,
// .quarantine-index-*, .oos-reserve.tmp), and all but the newest three <base>.corrupt-* copies.
// Names are anchored to those bases, so a state file placed in a shared
// directory never costs anyone else a file. Only regular files at the top
// level are touched; failures are skipped. It returns what it removed.
func SweepLeftovers(files []string, now time.Time) []string {
	type group struct {
		temp    []string // name prefixes of temp files
		corrupt []string // name prefixes of corrupt copies
	}
	groups := map[string]*group{}
	for _, f := range files {
		if f == "" {
			continue
		}
		dir, base := filepath.Dir(f), filepath.Base(f)
		g := groups[dir]
		if g == nil {
			// .oos-reserve.tmp is reserve.Name's temp, left by a crash
			// while the ENOSPC reserve was being written
			g = &group{temp: []string{".oos-record-", ".quarantine-index-", ".oos-reserve.tmp"}}
			groups[dir] = g
		}
		g.temp = append(g.temp, "."+base+".tmp-")
		g.corrupt = append(g.corrupt, base+".corrupt-")
	}
	var removed []string
	for dir, g := range groups {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		corrupt := map[string][]os.FileInfo{}
		for _, e := range ents {
			if !e.Type().IsRegular() {
				continue
			}
			name := e.Name()
			fi, err := e.Info()
			if err != nil {
				continue
			}
			if p := prefixOf(name, g.corrupt); p != "" {
				corrupt[p] = append(corrupt[p], fi)
				continue
			}
			if prefixOf(name, g.temp) != "" && now.Sub(fi.ModTime()) >= LeftoverAge {
				p := filepath.Join(dir, name)
				if os.Remove(p) == nil {
					removed = append(removed, p)
				}
			}
		}
		for _, fis := range corrupt {
			if len(fis) <= corruptKeep {
				continue
			}
			sort.Slice(fis, func(i, j int) bool { return fis[i].ModTime().After(fis[j].ModTime()) })
			for _, fi := range fis[corruptKeep:] {
				p := filepath.Join(dir, fi.Name())
				if os.Remove(p) == nil {
					removed = append(removed, p)
				}
			}
		}
	}
	sort.Strings(removed)
	return removed
}

func prefixOf(name string, prefixes []string) string {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return p
		}
	}
	return ""
}
