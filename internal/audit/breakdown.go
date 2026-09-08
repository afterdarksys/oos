package audit

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/size"
)

// AttributeDeep explains a directory nobody can label at the top level by
// looking at what is inside it. Children with a use case contribute their
// bytes to that label; unlabelled child directories are opened one more
// level while depth allows; whatever is left is "unattributed". With the
// size cache warm this costs a stat per directory, not a walk.
func AttributeDeep(cfg *config.Config, dir string, depth int, now time.Time) []UseCaseTotal {
	acc := map[string]*UseCaseTotal{}
	add := func(label string, bytes int64) {
		if label == "" {
			label = "unattributed"
		}
		t, ok := acc[label]
		if !ok {
			t = &UseCaseTotal{UseCase: label}
			acc[label] = t
		}
		t.Bytes += bytes
		t.Count++
	}
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		ents, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, de := range ents {
			p := filepath.Join(d, de.Name())
			fi, err := os.Lstat(p)
			if err != nil || fi.Mode()&os.ModeSymlink != 0 {
				continue
			}
			label, _ := cfg.UseCaseFor(p)
			size, _ := size.PathSize(p)
			if label != "" || !fi.IsDir() || depth == 0 {
				add(label, size)
				continue
			}
			walk(p, depth-1)
		}
	}
	walk(dir, depth)
	out := make([]UseCaseTotal, 0, len(acc))
	for _, t := range acc {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

// MixedLabel summarises a breakdown as "mixed: 61% uv / MCP, 30% Homebrew".
func MixedLabel(b []UseCaseTotal) string {
	var total int64
	for _, t := range b {
		total += t.Bytes
	}
	if total == 0 || len(b) == 0 {
		return ""
	}
	if len(b) == 1 && b[0].UseCase != "unattributed" {
		return b[0].UseCase
	}
	s := "mixed:"
	for i, t := range b {
		if i == 3 {
			break
		}
		pct := int(100 * t.Bytes / total)
		if pct < 5 {
			break
		}
		if i > 0 {
			s += ","
		}
		s += " " + itoa(pct) + "% " + t.UseCase
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// GroupByUseCaseDeep is groupByUseCase with breakdowns: a row that has one
// spreads its bytes across the labels inside it instead of landing whole
// under a single guess.
func GroupByUseCaseDeep(cfg *config.Config, rows []Row) []UseCaseTotal {
	acc := map[string]*UseCaseTotal{}
	add := func(label string, bytes int64) {
		if label == "" {
			label = "unattributed"
		}
		t, ok := acc[label]
		if !ok {
			t = &UseCaseTotal{UseCase: label}
			acc[label] = t
		}
		t.Bytes += bytes
		t.Count++
	}
	for _, r := range rows {
		if len(r.Breakdown) > 0 {
			for _, t := range r.Breakdown {
				add(t.UseCase, t.Bytes)
			}
			continue
		}
		add(r.UseCase, r.Bytes)
	}
	out := make([]UseCaseTotal, 0, len(acc))
	for _, t := range acc {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}
