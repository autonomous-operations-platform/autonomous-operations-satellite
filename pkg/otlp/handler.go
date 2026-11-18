package otlp

import (
	"compress/gzip"
	"encoding/json"
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

// NewHandler serves one endpoint on a server the caller runs. It ignores ep.Addr.
func NewHandler(ep Endpoint, st store.CollectorStore, logger *slog.Logger) (http.Handler, error) {
	cfg := Config{Logger: logger, Store: st, Endpoints: []Endpoint{ep}}.withDefaults()
	if cfg.Store == nil {
		return nil, fmt.Errorf("%w: Store is required", ErrInvalidConfig)
	}
	if err := cfg.Endpoints[0].validateWithoutAddr(); err != nil {
		return nil, err
	}
	return newMux(routes(cfg.Endpoints[0], cfg.Store, cfg.Logger)), nil
}

// routes registers each signal path twice. The method-less pattern catches
// non-POST requests, so they get an OTLP 405 body instead of the mux's plain text.
func routes(ep Endpoint, st store.CollectorStore, logger *slog.Logger) map[string]http.Handler {
	r := make(map[string]http.Handler, 2*len(ep.Signals))
	for _, sig := range ep.Signals {
		r["POST "+ep.signalPath(sig)] = &ingestHandler{
			ep:     ep,
			signal: sig,
			store:  st,
			logger: logger.With("endpoint", ep.Name, "signal", sig),
		}
		r[ep.signalPath(sig)] = http.HandlerFunc(methodNotAllowed)
	}
	return r
}

// newMux adds a catch-all, so unknown paths also get an OTLP error body.
func newMux(routes map[string]http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusNotFound, "not found")
	})
	for pattern, h := range routes {
		mux.Handle(pattern, h)
	}
	return mux
}

func methodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodPost)
	writeStatus(w, http.StatusMethodNotAllowed, "method not allowed")
}

// writeStatus writes an OTLP/HTTP error response: a JSON-encoded
// google.rpc.Status. OTLP does not use Status.code, so it is omitted.
func writeStatus(w http.ResponseWriter, status int, msg string) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Message string `json:"message"`
	}{msg})
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
	reject := func(e *httpError) {
		// Client errors log the fixed reason only: decoder errors can quote payload bytes.
		h.logger.WarnContext(ctx, "otlp: request rejected", "status", e.status, "reason", e.msg)
		writeStatus(w, e.status, e.msg)
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
			h.logger.ErrorContext(ctx, "otlp: processor failed", "err", err)
			writeStatus(w, http.StatusInternalServerError, "processing failed")
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
			h.logger.ErrorContext(ctx, "otlp: store write failed", "err", err)
			writeStatus(w, http.StatusServiceUnavailable, "storage unavailable")
			return
		}
	}

	h.logger.DebugContext(ctx, "otlp: accepted", "records", len(records), "bytes", len(body))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "{}")
}

// readBody limits both the raw and the decompressed bytes, so a small gzip
// body cannot expand without bound.
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
