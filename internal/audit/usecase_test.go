package audit

import (
	"testing"

	"github.com/afterdarksys/oos/internal/config"
)

func TestGroupByUseCase(t *testing.T) {
	cfg := &config.Config{Policy: config.Policy{Owners: []config.Owner{{Match: "/a", UseCase: "A"}}}}
	totals := GroupByUseCase(cfg, []string{"/a/x", "/a/y", "/b"}, []int64{5, 7, 3})
	if len(totals) != 2 || totals[0].UseCase != "A" || totals[0].Bytes != 12 || totals[0].Count != 2 || totals[1].UseCase != "unattributed" {
		t.Errorf("totals = %+v", totals)
	}
}
