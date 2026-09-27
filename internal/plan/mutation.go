package plan

import (
	"context"
	"fmt"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/mutation"
	"github.com/afterdarksys/oos/internal/reserve"
	"github.com/afterdarksys/oos/internal/state"
	"path/filepath"
	"time"
)

func Mutation(cfg *config.Config) (*mutation.Lock, error) {
	home := cfg.Home
	if home == "" {
		home = filepath.Dir(cfg.Policy.LogFile)
	}
	return mutation.Acquire(home)
}

// PurgeConfigured is the scheduled purge entrypoint; it shares the CLI lock.
// A purge is a permanent delete, so on a full disk its audit may fall back
// to the space reserve and then to stderr.
func PurgeConfigured(cfg *config.Config, now time.Time) (int64, []string, error) {
	l, err := Mutation(cfg)
	if err != nil {
		return 0, nil, err
	}
	defer l.Close()
	log, err := state.OpenLog(cfg.Policy.LogFile)
	if err != nil {
		return 0, nil, err
	}
	defer log.Close()
	audit := &auditLog{w: log, reserve: reserve.Path(cfg.Policy.StateFile), permanent: true}
	if err = audit.line(fmt.Sprintf("%s scheduled purge intent", now.UTC().Format(time.RFC3339)), true); err != nil {
		return 0, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Policy.OperationTimeout())
	defer cancel()
	n, names, err := PurgeStores(ctx, cfg.Policy, time.Duration(cfg.Policy.QuarantineDays)*24*time.Hour, now, false, false)
	errText := "<nil>"
	if err != nil {
		errText = err.Error()
	}
	_ = audit.line(fmt.Sprintf("%s scheduled purge recorded=%d batches=%q err=%q", now.UTC().Format(time.RFC3339), n, names, errText), true)
	return n, names, err
}
