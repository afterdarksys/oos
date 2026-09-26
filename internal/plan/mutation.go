package plan

import (
	"context"
	"fmt"
	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/mutation"
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
	if _, err = fmt.Fprintf(log, "%s scheduled purge intent\n", now.UTC().Format(time.RFC3339)); err != nil {
		return 0, nil, err
	}
	if err = log.Sync(); err != nil {
		return 0, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Policy.OperationTimeout())
	defer cancel()
	n, names, err := PurgeStores(ctx, cfg.Policy, time.Duration(cfg.Policy.QuarantineDays)*24*time.Hour, now, false, false)
	fmt.Fprintf(log, "%s scheduled purge recorded=%d batches=%v err=%v\n", now.UTC().Format(time.RFC3339), n, names, err)
	return n, names, err
}
