package otlp

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

// NewHandler returns a standard net/http handler that serves one endpoint's signal paths.
func NewHandler(ep Endpoint, st store.CollectorStore, logger *slog.Logger) (http.Handler, error) {
	cfg := Config{Logger: logger, Store: st, Endpoints: []Endpoint{ep}}.withDefaults()
	if cfg.Store == nil {
		return nil, fmt.Errorf("%w: Store is required", ErrInvalidConfig)
	}
	if err := cfg.Endpoints[0].validate(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	register(mux, cfg.Endpoints[0], cfg.Store, cfg.Logger)
	return mux, nil
}

// register adds one route per signal of ep to mux.
func register(mux *http.ServeMux, ep Endpoint, st store.CollectorStore, logger *slog.Logger) {
	for _, sig := range ep.Signals {
		mux.Handle("POST "+ep.signalPath(sig), &ingestHandler{ep: ep, signal: sig, store: st, logger: logger})
	}
}

type ingestHandler struct {
	ep     Endpoint
	signal store.Signal
	store  store.CollectorStore
	logger *slog.Logger
}

// httpError is a client-visible failure. msg is fixed text that never
// contains internal error details.
type httpError struct {
	status int
	msg    string
}

func (h *ingestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	received := time.Now()
	ctx := r.Context()
	log := h.logger.With("endpoint", h.ep.Name, "signal", h.signal)
	reject := func(e *httpError) {
		// Client errors log the fixed reason only: decoder errors can quote payload bytes.
		log.WarnContext(ctx, "otlp: request rejected", "status", e.status, "reason", e.msg)
		http.Error(w, e.msg, e.status)
	}

	body, herr := readBody(w, r, h.ep.MaxBodyBytes)
	if herr != nil {
		reject(herr)
		return
	}
	records, err := decode(h.signal, body)
	if err != nil {
		reject(&httpError{http.StatusBadRequest, "invalid OTLP JSON body"})
		return
	}

	if h.ep.Processor != nil {
		u := *r.URL
		req := Request{
			Endpoint:   h.ep.Name,
			Signal:     h.signal,
			URL:        &u,
			Header:     r.Header.Clone(),
			RemoteAddr: r.RemoteAddr,
			Host:       r.Host,
			ReceivedAt: received,
		}
		if records, err = h.ep.Processor(ctx, req, records); err != nil {
			log.ErrorContext(ctx, "otlp: processor failed", "err", err)
			http.Error(w, "processing failed", http.StatusInternalServerError)
			return
		}
	}

	if len(records) > 0 {
		for i := range records {
			if records[i].Attributes == nil {
				records[i].Attributes = map[string]string{}
			}
		}
		if _, err := h.store.AddTelemetry(ctx, records); err != nil {
			log.ErrorContext(ctx, "otlp: store write failed", "err", err)
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
			return
		}
	}

	log.DebugContext(ctx, "otlp: accepted", "records", len(records), "bytes", len(body))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "{}")
}

// readBody checks the content type and encoding, then reads the body with
// the size limit applied to both the raw and the decompressed bytes.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, *httpError) {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
			return nil, &httpError{http.StatusUnsupportedMediaType, "unsupported content type: only application/json is accepted"}
		}
	}

	var src io.Reader = http.MaxBytesReader(w, r.Body, limit)
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(src)
		if err != nil {
			return nil, readError(err)
		}
		defer func() { _ = zr.Close() }()
		src = zr
	default:
		return nil, &httpError{http.StatusUnsupportedMediaType, "unsupported content encoding"}
	}

	body, err := io.ReadAll(io.LimitReader(src, limit+1))
	if err != nil {
		return nil, readError(err)
	}
	if int64(len(body)) > limit {
		return nil, &httpError{http.StatusRequestEntityTooLarge, "request body too large"}
	}
	if len(body) == 0 {
		return nil, &httpError{http.StatusBadRequest, "empty request body"}
	}
	return body, nil
}

func readError(err error) *httpError {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return &httpError{http.StatusRequestEntityTooLarge, "request body too large"}
	}
	if errors.Is(err, io.EOF) {
		return &httpError{http.StatusBadRequest, "empty request body"}
	}
	return &httpError{http.StatusBadRequest, "unreadable request body"}
}
