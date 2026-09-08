package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/afterdarksys/oos/internal/config"
	"github.com/afterdarksys/oos/internal/guard"
	"github.com/afterdarksys/oos/internal/size"
	"github.com/afterdarksys/oos/internal/testutil"
)

func TestServeAndQuery(t *testing.T) {
	home := t.TempDir()
	p := testutil.PolicyFor(home)
	p.StateFile = filepath.Join(home, "state.json")
	p.Daemon.SizedEveryHours = -1
	cfg := &config.Config{Version: 1, Volume: home, Policy: p, Home: home}
	now := time.Now()
	d := New(cfg, "test", guard.Env{Home: home}, "t", Deps{
		Disk: func(string) (size.DiskUsage, error) {
			return size.DiskUsage{Free: 40 * size.GB, Total: 100 * size.GB}, nil
		},
		Now: func() time.Time { return now },
	})
	d.Tick(now)
	sock := filepath.Join(home, "d.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, sock, d) }()
	var s Status
	var err error
	for i := 0; i < 50; i++ {
		if s, err = Query(sock, time.Second); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if s.FreeGB != 40 || s.Label != "OK" || s.Ticks != 1 || s.Version != "t" {
		t.Errorf("status over the socket: %+v", s)
	}
	// a second daemon on the same socket is refused
	if err := Serve(context.Background(), sock, d); err == nil {
		t.Error("a live socket must refuse a second daemon")
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("serve: %v", err)
	}
	if _, err := Query(sock, 200*time.Millisecond); err == nil {
		t.Error("socket must be gone after shutdown")
	}
	if _, err := Query("", time.Second); err == nil {
		t.Error("no socket configured is an error")
	}
}
