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
	ln, err := Listen(sock)
	if err != nil {
		return err
	}
	return ServeOn(ctx, ln, sock, d)
}

// ErrRunning is returned by Listen when a live daemon already answers.
var ErrRunning = errors.New("another oos daemon is running")

// Listen claims the socket. It fails when another daemon answers on it, or
// when the path holds something that is not a socket (which is never
// removed). An empty sock means no socket: a nil listener and no error.
func Listen(sock string) (net.Listener, error) {
	if sock == "" {
		return nil, nil
	}
	if _, err := Query(sock, time.Second); err == nil {
		return nil, fmt.Errorf("%w: it answers on %s", ErrRunning, sock)
	}
	if err := removeSocket(sock); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(sock, 0o600)
	return ln, nil
}

// removeSocket removes sock only when Lstat says it is a socket; a missing
// path is fine, anything else is refused rather than deleted.
func removeSocket(sock string) error {
	fi, err := os.Lstat(sock)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode().Type() != os.ModeSocket {
		return fmt.Errorf("%s exists and is not a socket; refusing to remove it", sock)
	}
	return os.Remove(sock)
}

// ServeOn serves on a listener from Listen until ctx ends. A nil listener
// (no socket configured) just waits.
func ServeOn(ctx context.Context, ln net.Listener, sock string, d *Daemon) error {
	if ln == nil {
		<-ctx.Done()
		return nil
	}
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
		_ = removeSocket(sock)
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
