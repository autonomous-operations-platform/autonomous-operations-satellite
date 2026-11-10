package otlp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
)

// Server is a running set of OTLP listeners started by Start.
type Server struct {
	listeners []net.Listener
	byName    map[string]int // endpoint name maps to index into listeners
	done      chan struct{}
	err       error // set before done is closed
}

// Start validates cfg, binds every listener, and serves in the background.
//
// Configuration and bind errors are returned before anything serves; on a
// bind error the listeners already bound are closed. Cancelling ctx shuts all
// listeners down gracefully within Config.ShutdownTimeout. An unexpected
// serve error on one listener shuts down the others. Wait reports the outcome,
// and the library also logs it, so callers need not watch Wait.
func Start(ctx context.Context, cfg Config) (*Server, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	// Group endpoints by address, keeping first-seen order.
	var addrs []string
	var muxes []*http.ServeMux
	addrIndex := make(map[string]int)
	s := &Server{byName: make(map[string]int), done: make(chan struct{})}
	for _, ep := range cfg.Endpoints {
		i, ok := addrIndex[ep.Addr]
		if !ok {
			i = len(addrs)
			addrIndex[ep.Addr] = i
			addrs = append(addrs, ep.Addr)
			muxes = append(muxes, http.NewServeMux())
		}
		register(muxes[i], ep, cfg.Store, cfg.Logger)
		s.byName[ep.Name] = i
	}

	var lc net.ListenConfig
	for _, addr := range addrs {
		ln, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			for _, l := range s.listeners {
				_ = l.Close()
			}
			return nil, fmt.Errorf("otlp: listen %s: %w", addr, err)
		}
		s.listeners = append(s.listeners, ln)
	}

	servers := make([]*http.Server, len(muxes))
	serveErrs := make(chan error, len(servers))
	var wg sync.WaitGroup
	for i, ln := range s.listeners {
		srv := &http.Server{
			Handler:           muxes[i],
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			ReadTimeout:       cfg.ReadTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
			ErrorLog:          slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelWarn),
		}
		servers[i] = srv
		cfg.Logger.InfoContext(ctx, "otlp: listening", "addr", ln.Addr().String())
		wg.Go(func() {
			if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				serveErrs <- fmt.Errorf("otlp: serve %s: %w", ln.Addr(), err)
			}
		})
	}

	go s.supervise(ctx, cfg, servers, serveErrs, &wg)
	return s, nil
}

// supervise waits for cancellation or the first serve error, then shuts
// every server down and records the joined result.
func (s *Server) supervise(ctx context.Context, cfg Config, servers []*http.Server, serveErrs chan error, wg *sync.WaitGroup) {
	var errs []error
	select {
	case <-ctx.Done():
	case err := <-serveErrs:
		errs = append(errs, err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	for i, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("otlp: shutdown %s: %w", s.listeners[i].Addr(), err))
			_ = srv.Close()
		}
	}
	wg.Wait()
	close(serveErrs)
	for err := range serveErrs {
		errs = append(errs, err)
	}

	s.err = errors.Join(errs...)
	if s.err != nil {
		cfg.Logger.ErrorContext(ctx, "otlp: server stopped with error", "err", s.err)
	} else {
		cfg.Logger.InfoContext(ctx, "otlp: server stopped")
	}
	close(s.done)
}

// Wait blocks until every listener has stopped. It returns nil after a clean
// shutdown, and the joined serve and shutdown errors otherwise.
func (s *Server) Wait() error {
	<-s.done
	return s.err
}

// Addr returns the bound address of the named endpoint, or nil when no
// endpoint has that name. It resolves ":0" to the real port.
func (s *Server) Addr(endpoint string) net.Addr {
	i, ok := s.byName[endpoint]
	if !ok {
		return nil
	}
	return s.listeners[i].Addr()
}
