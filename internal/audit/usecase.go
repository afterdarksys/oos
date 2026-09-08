package audit

import (
	"fmt"
	"io"
	"sort"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/size"
)

// UseCaseTotal is one row of a by-use-case summary.
type UseCaseTotal struct {
	UseCase string `json:"use_case"`
	Bytes   int64  `json:"bytes"`
	Count   int    `json:"count"`
}

// GroupByUseCase sums bytes per use case. Paths nobody can attribute land
// under "unattributed" so the gap is visible rather than hidden.
func GroupByUseCase(cfg *config.Config, paths []string, bytes []int64) []UseCaseTotal {
	acc := map[string]*UseCaseTotal{}
	for i, p := range paths {
		label, _ := cfg.UseCaseFor(p)
		if label == "" {
			label = "unattributed"
		}
		t, ok := acc[label]
		if !ok {
			t = &UseCaseTotal{UseCase: label}
			acc[label] = t
		}
		t.Bytes += bytes[i]
		t.Count++
	}
	out := make([]UseCaseTotal, 0, len(acc))
	for _, t := range acc {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

func PrintUseCaseTotals(out io.Writer, totals []UseCaseTotal) {
	if len(totals) == 0 {
		return
	}
	fmt.Fprintln(out, "by use case:")
	for _, t := range totals {
		fmt.Fprintf(out, "  %9s  %3d  %s\n", size.Human(t.Bytes), t.Count, t.UseCase)
	}
}
