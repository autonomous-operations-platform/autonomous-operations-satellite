package otlp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

// freeAddr returns a loopback address that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

type nopStore struct{}

func (nopStore) AddTelemetry(context.Context, []store.NewTelemetryRecord) ([]store.RecordID, error) {
	return nil, nil
}

// A listener that fails at runtime MUST NOT stop the other listeners of the
// same Start call. Wait reports the failure after cancellation.
func TestServer_RuntimeListenerFailure(t *testing.T) {
	logs := []store.Signal{store.SignalLogs}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv, err := Start(ctx, Config{Store: nopStore{}, Endpoints: []Endpoint{
		{Name: "a", Addr: "127.0.0.1:0", Signals: logs},
		{Name: "b", Addr: "localhost:0", Signals: logs},
	}})
	if err != nil {
		t.Fatal(err)
	}
	failed := srv.byName["a"]
	_ = failed.ln.Close() // simulate an accept failure on listener a
	<-failed.done

	resp, err := http.Post("http://"+srv.Addr("b").String()+"/v1/logs", "application/json",
		strings.NewReader(`{"resourceLogs":[]}`))
	if err != nil {
		t.Fatalf("listener b stopped after a's failure: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("listener b: got %d, want 200", resp.StatusCode)
	}

	cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Wait() }()
	select {
	case waitErr := <-done:
		if waitErr == nil {
			t.Fatal("Wait = nil, want the serve error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after cancellation")
	}
}

// A request that reaches a stopping Start call through a stale route gets a
// retryable 503 instead of running after the drain.
func TestDrain_RejectsAfterWait(t *testing.T) {
	d := newDrain(t.Context())
	h := d.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler ran after drain")
	}))
	if err := d.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/logs", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("got %d %q, want 503 JSON", rec.Code, rec.Body)
	}
}

// After a serve failure, a new listener can own the address. Stopping the
// failed listener's owner MUST NOT remove the new listener from the registry.
func TestUnregister_KeepsNewListener(t *testing.T) {
	logs := []store.Signal{store.SignalLogs}
	addr := freeAddr(t)
	start := func(ctx context.Context, name, basePath string) (*Server, error) {
		return Start(ctx, Config{Store: nopStore{}, Endpoints: []Endpoint{
			{Name: name, Addr: addr, BasePath: basePath, Signals: logs},
		}})
	}
	ctxA, cancelA := context.WithCancel(t.Context())
	a, err := start(ctxA, "a", "/a")
	if err != nil {
		t.Fatal(err)
	}
	failed := a.byName["a"]
	_ = failed.ln.Close()
	<-failed.done

	if _, err := start(t.Context(), "c", "/c"); err != nil {
		t.Fatalf("Start after failure: %v", err)
	}
	cancelA()
	if err := a.Wait(); err == nil {
		t.Fatal("Wait a = nil, want the serve error")
	}
	if _, err := start(t.Context(), "d", "/c"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("conflict with c: got %v, want ErrInvalidConfig", err)
	}
}

// A failed listener can sit in the registry until its serve goroutine
// removes it. Start MUST bind a new listener instead of joining it.
func TestBind_SkipsFailedListener(t *testing.T) {
	addr := freeAddr(t)
	dead := &listener{key: addr, done: make(chan struct{})}
	close(dead.done)
	registry.mu.Lock()
	registry.listeners[addr] = dead
	registry.mu.Unlock()
	t.Cleanup(func() {
		registry.mu.Lock()
		dead.unregister()
		registry.mu.Unlock()
	})

	srv, err := Start(t.Context(), Config{Store: nopStore{}, Endpoints: []Endpoint{
		{Name: "a", Addr: addr, Signals: []store.Signal{store.SignalLogs}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if srv.byName["a"] == dead {
		t.Fatal("Start joined a failed listener")
	}
}

// A failed listener MUST mark itself stopped before it waits for
// registry.mu, so bind sees the failure while a Start call holds the lock.
func TestServe_StopsBeforeLock(t *testing.T) {
	srv, err := Start(t.Context(), Config{Store: nopStore{}, Endpoints: []Endpoint{
		{Name: "a", Addr: "127.0.0.1:0", Signals: []store.Signal{store.SignalLogs}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	l := srv.byName["a"]
	registry.mu.Lock()
	defer registry.mu.Unlock()
	_ = l.ln.Close()
	select {
	case <-l.done:
	case <-time.After(time.Second):
		t.Fatal("done did not close while registry.mu was held")
	}
}
