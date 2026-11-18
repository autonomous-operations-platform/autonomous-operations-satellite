package otlp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

// Defaults applied to zero-valued Config and Endpoint fields.
const (
	DefaultBasePath          = "/v1"
	DefaultMaxBodyBytes      = int64(5 << 20) // 5 MiB
	DefaultShutdownTimeout   = 15 * time.Second
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultReadTimeout       = 30 * time.Second
	DefaultWriteTimeout      = 30 * time.Second
	DefaultIdleTimeout       = 120 * time.Second
)

// ErrInvalidConfig is wrapped by every configuration error from Start and NewHandler.
var ErrInvalidConfig = errors.New("otlp: invalid config")

// Config configures one Start call. Zero or negative durations use the defaults.
type Config struct {
	Logger *slog.Logger
	Store  store.CollectorStore
	// At least one is required.
	Endpoints []Endpoint

	ShutdownTimeout   time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// Endpoint is one OTLP/HTTP ingestion endpoint. It serves
// POST <BasePath>/<signal> for each signal in Signals.
type Endpoint struct {
	Name         string
	Addr         string // required by Start, ignored by NewHandler
	BasePath     string
	Signals      []store.Signal
	Processor    Processor
	MaxBodyBytes int64
}

// Request is the request metadata passed to a Processor. It is a copy, so a
// processor may change it without affecting the request.
type Request struct {
	Endpoint   string
	Signal     store.Signal
	URL        *url.URL
	Header     http.Header
	RemoteAddr string
	Host       string
	ReceivedAt time.Time
}

// Processor runs once per accepted request, after decoding and before the
// store write. It returns the records to store, which may be changed, fewer,
// or empty. An error aborts the request with status 500 and stores nothing.
type Processor func(ctx context.Context, req Request, records []store.NewTelemetryRecord) ([]store.NewTelemetryRecord, error)

func (c Config) withDefaults() Config {
	setDefault := func(field *time.Duration, defaultValue time.Duration) {
		if *field <= 0 {
			*field = defaultValue
		}
	}
	setDefault(&c.ShutdownTimeout, DefaultShutdownTimeout)
	setDefault(&c.ReadHeaderTimeout, DefaultReadHeaderTimeout)
	setDefault(&c.ReadTimeout, DefaultReadTimeout)
	setDefault(&c.WriteTimeout, DefaultWriteTimeout)
	setDefault(&c.IdleTimeout, DefaultIdleTimeout)
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	c.Endpoints = slices.Clone(c.Endpoints)
	for i := range c.Endpoints {
		c.Endpoints[i] = c.Endpoints[i].withDefaults()
	}
	return c
}

func (e Endpoint) withDefaults() Endpoint {
	if e.BasePath == "" {
		e.BasePath = DefaultBasePath
	}
	if e.MaxBodyBytes == 0 {
		e.MaxBodyBytes = DefaultMaxBodyBytes
	}
	e.Signals = slices.Clone(e.Signals)
	return e
}

func (e Endpoint) signalPath(sig store.Signal) string {
	return e.BasePath + "/" + string(sig)
}

// validate expects a Config from withDefaults.
func (c Config) validate() error {
	if c.Store == nil {
		return fmt.Errorf("%w: Store is required", ErrInvalidConfig)
	}
	if len(c.Endpoints) == 0 {
		return fmt.Errorf("%w: at least one endpoint is required", ErrInvalidConfig)
	}
	names := make(map[string]bool, len(c.Endpoints))
	paths := make(map[string]string) // addr + " " + path -> endpoint name
	for _, e := range c.Endpoints {
		if err := e.validate(); err != nil {
			return err
		}
		if names[e.Name] {
			return fmt.Errorf("%w: duplicate endpoint name %q", ErrInvalidConfig, e.Name)
		}
		names[e.Name] = true
		for _, sig := range e.Signals {
			key := e.Addr + " " + e.signalPath(sig)
			if other, dup := paths[key]; dup {
				return fmt.Errorf("%w: endpoints %q and %q both serve %s on %s",
					ErrInvalidConfig, other, e.Name, e.signalPath(sig), e.Addr)
			}
			paths[key] = e.Name
		}
	}
	return nil
}

// validate checks one endpoint for Start, including Addr.
func (e Endpoint) validate() error {
	if err := e.validateWithoutAddr(); err != nil {
		return err
	}
	if e.Addr == "" {
		return fmt.Errorf("%w: endpoint %q: Addr is required", ErrInvalidConfig, e.Name)
	}
	return nil
}

// validateWithoutAddr skips Addr, which NewHandler does not need.
func (e Endpoint) validateWithoutAddr() error {
	if e.Name == "" {
		return fmt.Errorf("%w: endpoint Name is required", ErrInvalidConfig)
	}
	bp := e.BasePath
	if !strings.HasPrefix(bp, "/") || strings.HasSuffix(bp, "/") ||
		path.Clean(bp) != bp || strings.ContainsAny(bp, " {}%") {
		// ServeMux unescapes %XX, so "/a%41" and "/aA" would collide and panic.
		return fmt.Errorf("%w: endpoint %q: BasePath %q must be a clean path that starts with / and has no trailing /, spaces, braces, or %%",
			ErrInvalidConfig, e.Name, bp)
	}
	if len(e.Signals) == 0 {
		return fmt.Errorf("%w: endpoint %q: at least one signal is required", ErrInvalidConfig, e.Name)
	}
	seen := make(map[store.Signal]bool, len(e.Signals))
	for _, sig := range e.Signals {
		switch sig {
		case store.SignalLogs, store.SignalMetrics, store.SignalTraces:
		default:
			return fmt.Errorf("%w: endpoint %q: unknown signal %q", ErrInvalidConfig, e.Name, sig)
		}
		if seen[sig] {
			return fmt.Errorf("%w: endpoint %q: duplicate signal %q", ErrInvalidConfig, e.Name, sig)
		}
		seen[sig] = true
	}
	if e.MaxBodyBytes < 0 {
		return fmt.Errorf("%w: endpoint %q: MaxBodyBytes must not be negative", ErrInvalidConfig, e.Name)
	}
	return nil
}
