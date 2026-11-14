package monitoring

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server serves the /metrics endpoint on a dedicated internal port,
// fully separated from the business port (docs/MONITORING_API.md 1.1).
type Server struct {
	bind    string
	enabled bool
	http    *http.Server

	mu       sync.Mutex // protects listener, stopDone and stopErr
	listener net.Listener
	stopDone chan struct{}
	stopErr  error
}

// newMetricsHTTPHandler restricts exposition to GET /metrics only.
func newMetricsHTTPHandler(registry *prometheus.Registry) *http.Server {
	mux := http.NewServeMux()
	metricsHandler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		metricsHandler.ServeHTTP(w, r)
	})
	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// NewServer creates an enabled metrics server for the given bind address and
// registry. It does not listen yet; Start binds synchronously for fail-fast.
func NewServer(bind string, registry *prometheus.Registry) *Server {
	return &Server{
		bind:    bind,
		enabled: true,
		http:    newMetricsHTTPHandler(registry),
	}
}

// StartEnabled does nothing if monitoring.metrics_enabled is false.
func (s *Server) StartEnabled() error {
	if !s.enabled {
		return nil
	}
	return s.Start()
}

// Start synchronously binds the configured address; a listen error is
// returned to the caller for fail-fast. Successful Serve runs in a goroutine.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopDone != nil {
		return errors.New("metrics server already stopped")
	}
	if s.listener != nil {
		return nil
	}

	listener, err := net.Listen("tcp", s.bind)
	if err != nil {
		return fmt.Errorf("metrics server listen %s: %w", s.bind, err)
	}
	s.listener = listener
	log.Printf("Metrics server listening on %s", listener.Addr())
	go func() {
		if serveErr := s.http.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("Metrics server serve error: %v", serveErr)
		}
	}()
	return nil
}

// Addr returns the bound address (empty before Start, including disabled).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Stop gracefully shuts down the metrics server. Concurrent callers wait for
// the first shutdown to finish and observe its result; stopping an unstarted
// server is a no-op. The context bounds the graceful shutdown.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	if done := s.stopDone; done != nil {
		s.mu.Unlock()
		<-done
		s.mu.Lock()
		err := s.stopErr
		s.mu.Unlock()
		return err
	}
	s.stopDone = make(chan struct{})
	listener := s.listener
	s.mu.Unlock()

	var err error
	if listener != nil {
		err = s.http.Shutdown(ctx)
	}

	s.mu.Lock()
	s.stopErr = err
	close(s.stopDone)
	s.mu.Unlock()
	return err
}
