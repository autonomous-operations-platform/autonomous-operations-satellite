package otlp_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/otlp"
)

func newHandler(t *testing.T, ep otlp.Endpoint, st store.CollectorStore) http.Handler {
	t.Helper()
	if ep.Name == "" {
		ep.Name = "test"
	}
	if ep.Signals == nil {
		ep.Signals = []store.Signal{store.SignalLogs, store.SignalMetrics, store.SignalTraces}
	}
	h, err := otlp.NewHandler(ep, st, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func post(h http.Handler, path string, body io.Reader, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, body)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func gzipped(t *testing.T, b []byte) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestHandler_Success(t *testing.T) {
	for _, tt := range []struct {
		path, body string
		signal     store.Signal
	}{
		{"/v1/logs", validLogs, store.SignalLogs},
		{"/v1/metrics", validMetrics, store.SignalMetrics},
		{"/v1/traces", validTraces, store.SignalTraces},
	} {
		t.Run(string(tt.signal), func(t *testing.T) {
			st := &fakeStore{}
			rec := post(newHandler(t, otlp.Endpoint{}, st), tt.path, strings.NewReader(tt.body),
				map[string]string{"Content-Type": "application/json; charset=utf-8"})
			if rec.Code != http.StatusOK || rec.Body.String() != "{}" {
				t.Fatalf("got %d %q, want 200 {}", rec.Code, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			calls, records := st.snapshot()
			if calls != 1 || len(records) != 1 || records[0].Signal != tt.signal {
				t.Errorf("store calls=%d records=%v", calls, records)
			}
		})
	}
}

// A batched logs push is stored with one AddTelemetry call holding every record.
func TestHandler_BatchedLogsSingleStoreCall(t *testing.T) {
	body, err := os.ReadFile("testdata/logs.json")
	if err != nil {
		t.Fatal(err)
	}
	st := &fakeStore{}
	rec := post(newHandler(t, otlp.Endpoint{}, st), "/v1/logs", bytes.NewReader(body),
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %q, want 200", rec.Code, rec.Body)
	}
	if calls, records := st.snapshot(); calls != 1 || len(records) != 3 {
		t.Errorf("store calls=%d records=%d, want 1 call with 3 records", calls, len(records))
	}
}

func TestHandler_MissingContentTypeIsJSON(t *testing.T) {
	st := &fakeStore{}
	rec := post(newHandler(t, otlp.Endpoint{}, st), "/v1/logs", strings.NewReader(validLogs), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}

func TestHandler_Rejections(t *testing.T) {
	big := `{"resourceLogs":[],"pad":"` + strings.Repeat("x", 100) + `"}`
	bomb := gzipped(t, []byte(`{"pad":"`+strings.Repeat("x", 10_000)+`"}`))
	tests := []struct {
		name   string
		body   io.Reader
		header map[string]string
		status int
	}{
		{"protobuf", strings.NewReader(validLogs), map[string]string{"Content-Type": "application/x-protobuf"}, http.StatusUnsupportedMediaType},
		{"text", strings.NewReader(validLogs), map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		{"malformed content type", strings.NewReader(validLogs), map[string]string{"Content-Type": "application/json; ="}, http.StatusUnsupportedMediaType},
		{"brotli", strings.NewReader(validLogs), map[string]string{"Content-Encoding": "br"}, http.StatusUnsupportedMediaType},
		{"empty", strings.NewReader(""), nil, http.StatusBadRequest},
		{"empty gzip", strings.NewReader(""), map[string]string{"Content-Encoding": "gzip"}, http.StatusBadRequest},
		{"invalid json", strings.NewReader("{not json"), nil, http.StatusBadRequest},
		{"oversized raw", strings.NewReader(big), nil, http.StatusRequestEntityTooLarge},
		{"oversized after gunzip", gzipped(t, []byte(big+strings.Repeat(" ", 5000))), map[string]string{"Content-Encoding": "gzip"}, http.StatusRequestEntityTooLarge},
		{"gzip bomb", bomb, map[string]string{"Content-Encoding": "gzip"}, http.StatusRequestEntityTooLarge},
		{"corrupt gzip", strings.NewReader("not gzip at all"), map[string]string{"Content-Encoding": "gzip"}, http.StatusBadRequest},
		{"truncated gzip", bytes.NewReader(gzipped(t, []byte(validLogs)).Bytes()[:20]), map[string]string{"Content-Encoding": "gzip"}, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStore{}
			// Limit 100 bytes: the bomb is far smaller compressed but expands past it.
			rec := post(newHandler(t, otlp.Endpoint{MaxBodyBytes: 100}, st), "/v1/logs", tt.body, tt.header)
			if rec.Code != tt.status {
				t.Fatalf("got %d (%q), want %d", rec.Code, rec.Body, tt.status)
			}
			if calls, _ := st.snapshot(); calls != 0 {
				t.Errorf("store called %d times on a rejected request", calls)
			}
		})
	}
}

func TestHandler_BodyAtLimitAccepted(t *testing.T) {
	st := &fakeStore{}
	h := newHandler(t, otlp.Endpoint{MaxBodyBytes: int64(len(validLogs))}, st)
	if rec := post(h, "/v1/logs", strings.NewReader(validLogs), nil); rec.Code != http.StatusOK {
		t.Fatalf("body at limit: got %d", rec.Code)
	}
	h = newHandler(t, otlp.Endpoint{MaxBodyBytes: int64(len(validLogs)) - 1}, st)
	if rec := post(h, "/v1/logs", strings.NewReader(validLogs), nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body one byte over limit: got %d", rec.Code)
	}
}

func TestHandler_Gzip(t *testing.T) {
	st := &fakeStore{}
	rec := post(newHandler(t, otlp.Endpoint{}, st), "/v1/logs", gzipped(t, []byte(validLogs)),
		map[string]string{"Content-Encoding": "GZIP"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if _, records := st.snapshot(); len(records) != 1 || string(records[0].Payload) != "hello" {
		t.Errorf("records = %v", records)
	}
}

func TestHandler_ProcessorMetadata(t *testing.T) {
	var got otlp.Request
	before := time.Now()
	ep := otlp.Endpoint{
		Name:     "lb",
		BasePath: "/ingest/lb",
		Processor: func(_ context.Context, req otlp.Request, records []store.NewTelemetryRecord) ([]store.NewTelemetryRecord, error) {
			got = req
			req.Header.Set("X-Source", "changed")
			req.URL.Path = "/changed"
			for i := range records {
				records[i].Attributes["source"] = "lb"
			}
			return records, nil
		},
	}
	st := &fakeStore{}
	h := newHandler(t, ep, st)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://sat.example.com/ingest/lb/logs?tenant=t1", strings.NewReader(validLogs))
	req.Header.Set("X-Source", "lb1")
	req.RemoteAddr = "192.0.2.10:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}

	if got.Endpoint != "lb" || got.Signal != store.SignalLogs {
		t.Errorf("endpoint/signal = %q/%q", got.Endpoint, got.Signal)
	}
	if got.URL.Query().Get("tenant") != "t1" {
		t.Errorf("URL = %v", got.URL)
	}
	if got.RemoteAddr != "192.0.2.10:5555" || got.Host != "sat.example.com" {
		t.Errorf("remote/host = %q/%q", got.RemoteAddr, got.Host)
	}
	if got.ReceivedAt.Before(before) || got.ReceivedAt.After(time.Now()) {
		t.Errorf("ReceivedAt = %v", got.ReceivedAt)
	}
	// The processor changed its copy; the request must be untouched.
	if req.Header.Get("X-Source") != "lb1" || req.URL.Path != "/ingest/lb/logs" {
		t.Errorf("processor changes leaked into the request: %q %q", req.Header.Get("X-Source"), req.URL.Path)
	}
	if _, records := st.snapshot(); records[0].Attributes["source"] != "lb" {
		t.Errorf("enrichment not stored: %v", records[0].Attributes)
	}
}

func TestHandler_ProcessorDropsAll(t *testing.T) {
	st := &fakeStore{}
	ep := otlp.Endpoint{Processor: func(context.Context, otlp.Request, []store.NewTelemetryRecord) ([]store.NewTelemetryRecord, error) {
		return nil, nil
	}}
	rec := post(newHandler(t, ep, st), "/v1/logs", strings.NewReader(validLogs), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if calls, _ := st.snapshot(); calls != 0 {
		t.Errorf("store called %d times", calls)
	}
}

func TestHandler_ProcessorNilAttributesNormalized(t *testing.T) {
	st := &fakeStore{}
	ep := otlp.Endpoint{Processor: func(context.Context, otlp.Request, []store.NewTelemetryRecord) ([]store.NewTelemetryRecord, error) {
		return []store.NewTelemetryRecord{{Signal: store.SignalLogs}}, nil
	}}
	post(newHandler(t, ep, st), "/v1/logs", strings.NewReader(validLogs), nil)
	if _, records := st.snapshot(); len(records) != 1 || records[0].Attributes == nil {
		t.Errorf("want non-nil attributes, got %v", records)
	}
}

func TestHandler_EmptyLogsRequestStoresNothing(t *testing.T) {
	st := &fakeStore{}
	rec := post(newHandler(t, otlp.Endpoint{}, st), "/v1/logs", strings.NewReader(`{"resourceLogs":[]}`), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if calls, _ := st.snapshot(); calls != 0 {
		t.Errorf("store called %d times", calls)
	}
}

func TestHandler_InternalErrorsHidden(t *testing.T) {
	const dbHost = "db.example.com:9030"
	tests := []struct {
		name   string
		ep     otlp.Endpoint
		st     *fakeStore
		status int
	}{
		{"processor", otlp.Endpoint{Processor: func(context.Context, otlp.Request, []store.NewTelemetryRecord) ([]store.NewTelemetryRecord, error) {
			return nil, errors.New("dial " + dbHost)
		}}, &fakeStore{}, http.StatusInternalServerError},
		{"store", otlp.Endpoint{}, &fakeStore{err: errors.New("dial " + dbHost)}, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := post(newHandler(t, tt.ep, tt.st), "/v1/logs", strings.NewReader(validLogs), nil)
			if rec.Code != tt.status {
				t.Fatalf("got %d, want %d", rec.Code, tt.status)
			}
			if strings.Contains(rec.Body.String(), dbHost) {
				t.Errorf("response leaks internal error: %q", rec.Body)
			}
			if tt.name == "processor" {
				if calls, _ := tt.st.snapshot(); calls != 0 {
					t.Errorf("store called after processor error")
				}
			}
		})
	}
}

func TestHandler_Routing(t *testing.T) {
	h := newHandler(t, otlp.Endpoint{Signals: []store.Signal{store.SignalLogs}}, &fakeStore{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/logs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET: got %d Allow=%q, want 405 Allow=POST", rec.Code, rec.Header().Get("Allow"))
	}

	if rec := post(h, "/v1/traces", strings.NewReader(validTraces), nil); rec.Code != http.StatusNotFound {
		t.Errorf("unconfigured signal: got %d, want 404", rec.Code)
	}
}

// The decompressed limit must stop reading early, not buffer the whole bomb
// and reject it afterwards.
func TestHandler_GzipBombBoundedMemory(t *testing.T) {
	bomb := gzipped(t, make([]byte, 16<<20)) // 16 MiB of zeros, about 16 KiB compressed
	h := newHandler(t, otlp.Endpoint{MaxBodyBytes: 1 << 20}, &fakeStore{})

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rec := post(h, "/v1/logs", bomb, map[string]string{"Content-Encoding": "gzip"})
	runtime.ReadMemStats(&after)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %d, want 413", rec.Code)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Errorf("handler allocated %d MiB for a 1 MiB limit", allocated>>20)
	}
}
