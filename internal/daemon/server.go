package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Serve answers /status and /health on a unix socket until ctx ends. A live
// daemon already on the socket is an error; a dead socket file is removed.
func Serve(ctx context.Context, sock string, d *Daemon) error {
	if sock == "" {
		<-ctx.Done()
		return nil
	}
	if _, err := Query(sock, time.Second); err == nil {
		return fmt.Errorf("another oos daemon answers on %s", sock)
	}
	_ = os.Remove(sock)
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		return err
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(sock, 0o600)
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.Snapshot())
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		s := d.Snapshot()
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "%s %.1f\n", s.Label, s.FreeGB)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
		_ = os.Remove(sock)
	}()
	d.logf("listening on %s", sock)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Query asks a running daemon for its status.
func Query(sock string, timeout time.Duration) (Status, error) {
	var s Status
	if sock == "" {
		return s, errors.New("no socket configured")
	}
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dd net.Dialer
			return dd.DialContext(ctx, "unix", sock)
		},
	}}
	resp, err := client.Get("http://oos/status")
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return s, fmt.Errorf("daemon answered %s", resp.Status)
	}
	return s, json.NewDecoder(resp.Body).Decode(&s)
}
