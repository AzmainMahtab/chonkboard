// Package httpserver owns the HTTP surface: the middleware stack, the route
// table, and the listener's lifecycle. Every new top-level route prefix is
// declared here rather than in main, so one file answers "what does this app
// serve?".
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

type Config struct {
	Addr            string
	ShutdownTimeout time.Duration
}

type Server struct {
	http *http.Server
	log  *slog.Logger
	cfg  Config
}

func New(cfg Config, log *slog.Logger, handler http.Handler) *Server {
	return &Server{
		cfg: cfg,
		log: log,
		http: &http.Server{
			Addr:    cfg.Addr,
			Handler: handler,
			// No WriteTimeout: an SSE stream is a long-lived response and a
			// write deadline would sever it mid-board. Idle clients are culled
			// by the heartbeat instead.
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
	}
}

// Run blocks until ctx is cancelled, then drains in-flight requests. Open SSE
// streams end when their request contexts are cancelled by Shutdown.
func (s *Server) Run(ctx context.Context) error {
	errs := make(chan error, 1)
	go func() {
		s.log.Info("listening", "addr", s.cfg.Addr)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	s.log.Info("shutting down", "timeout", s.cfg.ShutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		// Streams still open past the grace period are closed hard.
		_ = s.http.Close()
		return err
	}
	return nil
}
