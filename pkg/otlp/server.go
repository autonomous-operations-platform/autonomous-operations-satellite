package otlp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// registry maps each Addr to the listener that all Start calls on it share.
//
//nolint:gochecknoglobals // a port is process-wide, so listener sharing across Start calls is too
var registry = struct {
	mu        sync.Mutex
	listeners map[string]*listener
}{listeners: make(map[string]*listener)}

// timeouts belong to the http.Server, so Start calls sharing a listener must agree on them.
type timeouts struct {
	readHeader, read, write, idle time.Duration
}

func (c Config) timeouts() timeouts {
	return timeouts{c.ReadHeaderTimeout, c.ReadTimeout, c.WriteTimeout, c.IdleTimeout}
}

type listener struct {
	key      string // empty for port 0, which is never shared
	ln       net.Listener
	srv      *http.Server
	timeouts timeouts
	owners   map[*Server]map[string]http.Handler // routes per Start call, guarded by registry.mu
	mux      atomic.Pointer[http.ServeMux]       // swapped, never mutated: ServeMux cannot unregister routes
	done     chan struct{}
	err      error // written before done closes
}

func newListener(ctx context.Context, addr string, cfg Config) (*listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("otlp: listen %s: %w", addr, err)
	}
	l := &listener{
		ln:       ln,
		timeouts: cfg.timeouts(),
		owners:   make(map[*Server]map[string]http.Handler),
		done:     make(chan struct{}),
	}
	if _, port, _ := net.SplitHostPort(addr); port != "0" {
		l.key = addr
	}
	l.srv = &http.Server{
		Handler:           l,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelWarn),
	}
	return l, nil
}

func (l *listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mux.Load().ServeHTTP(w, r)
}

// checkJoin requires registry.mu.
func (l *listener) checkJoin(addr string, want timeouts, routes map[string]http.Handler) error {
	if l.timeouts != want {
		return fmt.Errorf("%w: %s is in use by another Start call with different timeouts", ErrInvalidConfig, addr)
	}
	for _, owned := range l.owners {
		for pattern := range routes {
			if _, dup := owned[pattern]; dup {
				return fmt.Errorf("%w: %s on %s is in use by another Start call", ErrInvalidConfig, pattern, addr)
			}
		}
	}
	return nil
}

// rebuild requires registry.mu.
func (l *listener) rebuild() {
	all := make(map[string]http.Handler)
	for _, routes := range l.owners {
		maps.Copy(all, routes)
	}
	l.mux.Store(newMux(all))
}

// unregister requires registry.mu. After a serve failure a new listener may
// already own the key, so it deletes only its own entry.
func (l *listener) unregister() {
	if l.key != "" && registry.listeners[l.key] == l {
		delete(registry.listeners, l.key)
	}
}

// serve deliberately does not stop the owners on failure: their other
// listeners stay up, and a later Start call can bind the address again.
// It closes done before taking registry.mu, so bind sees the failure even
// while the failed listener is still registered.
func (l *listener) serve(ctx context.Context, logger *slog.Logger) {
	err := l.srv.Serve(l.ln)
	if errors.Is(err, http.ErrServerClosed) {
		close(l.done)
		return
	}
	l.err = fmt.Errorf("otlp: serve %s: %w", l.ln.Addr(), err)
	close(l.done)
	logger.ErrorContext(ctx, "otlp: listener stopped", "err", l.err)
	registry.mu.Lock()
	l.unregister()
	registry.mu.Unlock()
}

// stopped reports whether Serve has returned. A registered listener only
// stops by failing.
func (l *listener) stopped() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// Server is the handle of one Start call.
type Server struct {
	listeners map[string]*listener // keyed by Addr
	byName    map[string]*listener
	inflight  *drain
	done      chan struct{}
	err       error // written before done closes
}

// Start validates cfg, binds the listeners, and serves in the background.
// It returns configuration and bind errors before this call serves anything.
//
// Start calls with the same Addr string share one listener. Their signal
// paths must differ and their timeouts must match, or Start returns
// ErrInvalidConfig. Port 0 always binds a new port. A shared listener logs
// to the Logger of the call that bound it. Start binds under a process-wide
// lock, so an Addr with a host name blocks other calls during DNS lookup.
//
// Cancelling ctx removes this call's routes and drains its in-flight
// requests within Config.ShutdownTimeout. Requests still running after
// that get a cancelled context. A serve error stops only the failed
// listener. The library logs it, and Wait reports it.
func Start(ctx context.Context, cfg Config) (*Server, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	s := &Server{
		listeners: make(map[string]*listener),
		byName:    make(map[string]*listener),
		inflight:  newDrain(ctx),
		done:      make(chan struct{}),
	}
	routesByAddr := make(map[string]map[string]http.Handler)
	for _, ep := range cfg.Endpoints {
		if routesByAddr[ep.Addr] == nil {
			routesByAddr[ep.Addr] = make(map[string]http.Handler)
		}
		for pattern, h := range routes(ep, cfg.Store, cfg.Logger) {
			routesByAddr[ep.Addr][pattern] = s.inflight.wrap(h)
		}
	}

	// Held across Listen, so two concurrent Start calls on one Addr cannot
	// both bind.
	registry.mu.Lock()
	defer registry.mu.Unlock()
	fresh, err := s.bind(ctx, cfg, routesByAddr)
	if err != nil {
		return nil, err
	}
	for addr, l := range s.listeners {
		l.owners[s] = routesByAddr[addr]
		l.rebuild()
	}
	for _, l := range fresh {
		if l.key != "" {
			registry.listeners[l.key] = l
		}
		cfg.Logger.InfoContext(ctx, "otlp: listening", "addr", l.ln.Addr().String())
		go l.serve(context.WithoutCancel(ctx), cfg.Logger)
	}
	for _, ep := range cfg.Endpoints {
		s.byName[ep.Name] = s.listeners[ep.Addr]
	}

	go s.supervise(ctx, cfg)
	return s, nil
}

// bind requires registry.mu. It checks all shared listeners before binding
// any new one, so a conflict leaves nothing to close. It binds in
// cfg.Endpoints order, so the first failing address is reported.
func (s *Server) bind(ctx context.Context, cfg Config, routesByAddr map[string]map[string]http.Handler) ([]*listener, error) {
	for _, ep := range cfg.Endpoints {
		if l := registry.listeners[ep.Addr]; l != nil && !l.stopped() {
			if err := l.checkJoin(ep.Addr, cfg.timeouts(), routesByAddr[ep.Addr]); err != nil {
				return nil, err
			}
			s.listeners[ep.Addr] = l
		}
	}
	var fresh []*listener
	for _, ep := range cfg.Endpoints {
		if s.listeners[ep.Addr] != nil {
			continue
		}
		l, err := newListener(ctx, ep.Addr, cfg)
		if err != nil {
			for _, b := range fresh {
				_ = b.ln.Close()
			}
			return nil, err
		}
		s.listeners[ep.Addr] = l
		fresh = append(fresh, l)
	}
	return fresh, nil
}

func (s *Server) supervise(ctx context.Context, cfg Config) {
	<-ctx.Done()
	last := s.detach()

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	var errs []error
	// Shutdown first: it closes the port at once, so a restart can bind it.
	for _, l := range last {
		if err := l.srv.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("otlp: shutdown %s: %w", l.ln.Addr(), err))
			_ = l.srv.Close()
		}
		<-l.done
	}
	// A shutdown error already reports the same deadline.
	if err := s.inflight.wait(shutdownCtx); err != nil && len(errs) == 0 {
		errs = append(errs, fmt.Errorf("otlp: drain requests: %w", err))
	}
	for _, l := range s.listeners {
		if l.stopped() && l.err != nil {
			errs = append(errs, l.err)
		}
	}

	s.err = errors.Join(errs...)
	if s.err != nil {
		cfg.Logger.ErrorContext(ctx, "otlp: server stopped with error", "err", s.err)
	} else {
		cfg.Logger.InfoContext(ctx, "otlp: server stopped")
	}
	close(s.done)
}

// detach returns the listeners this call was the last owner of.
func (s *Server) detach() []*listener {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	var last []*listener
	for _, l := range s.listeners {
		delete(l.owners, s)
		if len(l.owners) == 0 {
			l.unregister()
			last = append(last, l)
		} else {
			l.rebuild()
		}
	}
	return last
}

// Wait blocks until ctx is cancelled and this call has stopped. It returns
// the joined serve, drain, and shutdown errors.
func (s *Server) Wait() error {
	<-s.done
	return s.err
}

// Addr returns the bound address of the named endpoint, or nil when no
// endpoint has that name. It resolves ":0" to the real port.
func (s *Server) Addr(endpoint string) net.Addr {
	l, ok := s.byName[endpoint]
	if !ok {
		return nil
	}
	return l.ln.Addr()
}

// drain waits for one Start call's requests. http.Server.Shutdown cannot,
// because it would close a listener that other calls still use.
type drain struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
	ctx    context.Context // cancels requests left running when wait returns
	cancel context.CancelFunc
}

func newDrain(parent context.Context) *drain {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	return &drain{ctx: ctx, cancel: cancel}
}

// wrap answers 503 once wait has begun: a request can still reach a stale mux.
func (d *drain) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			writeStatus(w, http.StatusServiceUnavailable, "endpoint is shutting down")
			return
		}
		d.wg.Add(1)
		d.mu.Unlock()
		defer d.wg.Done()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		defer context.AfterFunc(d.ctx, cancel)()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// wait cancels the requests still running when it returns.
func (d *drain) wait(ctx context.Context) error {
	defer d.cancel()
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
