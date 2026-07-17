// Package api exposes the daemon's last-reconcile status over a local HTTP
// endpoint (a unix socket by default) so puppet's facter — or a health check —
// can see whether records are actually live on the server, without re-querying
// DNS itself. It reports results and health, not the desired record set (puppet
// already owns that, it writes the records file).
package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ZoneStatus is the outcome of the last update for one zone.
type ZoneStatus struct {
	Zone      string `json:"zone"`
	Adds      int    `json:"adds"`
	Deletes   int    `json:"deletes"`
	Rcode     int    `json:"rcode"`
	RcodeText string `json:"rcode_text"`
	Error     string `json:"error,omitempty"`
}

// Status is the daemon's current view, serialised to JSON.
type Status struct {
	Version        string       `json:"version"`
	Server         string       `json:"server"`
	RecordsFile    string       `json:"records_file"`
	Healthy        bool         `json:"healthy"`
	ManagedRecords int          `json:"managed_records"`
	LastReconcile  time.Time    `json:"last_reconcile"`
	LastChange     time.Time    `json:"last_change,omitempty"`
	LastError      string       `json:"last_error,omitempty"`
	Zones          []ZoneStatus `json:"zones"`
}

// Store holds the latest Status behind a mutex.
type Store struct {
	mu sync.RWMutex
	s  Status
}

// NewStore seeds a Store with static fields.
func NewStore(version, server, recordsFile string) *Store {
	return &Store{s: Status{Version: version, Server: server, RecordsFile: recordsFile}}
}

// Set replaces the dynamic portion of the status.
func (st *Store) Set(s Status) {
	st.mu.Lock()
	defer st.mu.Unlock()
	// preserve static identity fields
	s.Version, s.Server, s.RecordsFile = st.s.Version, st.s.Server, st.s.RecordsFile
	st.s = s
}

// Get returns a copy of the current status.
func (st *Store) Get() Status {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.s
}

// Serve starts an HTTP server on addr. If addr contains a '/', it is treated as
// a unix socket path; otherwise as a TCP address. It returns once ctx is done.
func Serve(ctx context.Context, addr string, store *Store) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, store.Get())
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		s := store.Get()
		code := http.StatusOK
		if !s.Healthy {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]any{"healthy": s.Healthy, "last_error": s.LastError})
	})

	ln, err := listen(addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func listen(addr string) (net.Listener, error) {
	if strings.Contains(addr, "/") {
		if err := os.MkdirAll(filepath.Dir(addr), 0o755); err != nil {
			return nil, err
		}
		_ = os.Remove(addr) // clear a stale socket from an unclean exit
		ln, err := net.Listen("unix", addr)
		if err != nil {
			return nil, err
		}
		_ = os.Chmod(addr, 0o660)
		return ln, nil
	}
	return net.Listen("tcp", addr)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
