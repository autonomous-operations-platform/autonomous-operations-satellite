package otlp_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/otlp"
)

// logsSignals returns a fresh signal list with only logs.
func logsSignals() []store.Signal { return []store.Signal{store.SignalLogs} }

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

func assertClosed(t *testing.T, addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Errorf("%s still in use: %v", addr, err)
		return
	}
	_ = ln.Close()
}

func postURL(t *testing.T, url, body string) (int, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func waitDone(t *testing.T, srv *otlp.Server) error {
	t.Helper()
	ch := make(chan error, 1)
	go func() { ch <- srv.Wait() }()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return in time")
		return nil
	}
}

func TestStart_PortInUse(t *testing.T) {
	busy := func() net.Listener {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		return ln
	}
	busy1, busy2 := busy(), busy()
	first := freeAddr(t)

	srv, err := otlp.Start(t.Context(), otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{
		{Name: "a", Addr: first, Signals: logsSignals()},
		{Name: "b", Addr: busy1.Addr().String(), Signals: logsSignals()},
		{Name: "c", Addr: busy2.Addr().String(), Signals: logsSignals()},
	}})
	if err == nil || srv != nil {
		t.Fatalf("want bind error and nil server, got %v, %v", srv, err)
	}
	// Endpoints bind in config order, so the first busy address fails.
	if !strings.Contains(err.Error(), busy1.Addr().String()) {
		t.Errorf("error %q does not name %s", err, busy1.Addr())
	}
	assertClosed(t, first)
}

func TestStart_CleanShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	srv, err := otlp.Start(ctx, otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{
		{Name: "a", Addr: "127.0.0.1:0", Signals: logsSignals()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	addr := srv.Addr("a").String()
	if code, err := postURL(t, "http://"+addr+"/v1/logs", validLogs); err != nil || code != http.StatusOK {
		t.Fatalf("post: %d %v", code, err)
	}
	if srv.Addr("missing") != nil {
		t.Error("Addr of unknown endpoint must be nil")
	}
	cancel()
	if err := waitDone(t, srv); err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
	assertClosed(t, addr)
}

// blockingProcessor signals entered and blocks until release is closed.
func blockingProcessor(entered chan<- struct{}, release <-chan struct{}) otlp.Processor {
	var once sync.Once
	return func(_ context.Context, _ otlp.Request, r []store.NewTelemetryRecord) ([]store.NewTelemetryRecord, error) {
		once.Do(func() { close(entered) })
		<-release
		return r, nil
	}
}

func TestStart_InFlightRequestDrains(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	st := &fakeStore{}
	ctx, cancel := context.WithCancel(t.Context())
	srv, err := otlp.Start(ctx, otlp.Config{Store: st, Endpoints: []otlp.Endpoint{
		{Name: "a", Addr: "127.0.0.1:0", Signals: logsSignals(), Processor: blockingProcessor(entered, release)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		code, _ := postURL(t, "http://"+srv.Addr("a").String()+"/v1/logs", validLogs)
		result <- code
	}()
	<-entered
	cancel()
	time.Sleep(20 * time.Millisecond) // let Shutdown begin while the request is in flight
	close(release)

	if code := <-result; code != http.StatusOK {
		t.Errorf("in-flight request got %d, want 200", code)
	}
	if err := waitDone(t, srv); err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
	if _, records := st.snapshot(); len(records) != 1 {
		t.Errorf("in-flight store write lost: %d records", len(records))
	}
}

func TestStart_ShutdownTimeout(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(t.Context())
	srv, err := otlp.Start(ctx, otlp.Config{
		Store:           &fakeStore{},
		ShutdownTimeout: 50 * time.Millisecond,
		Endpoints: []otlp.Endpoint{
			{Name: "a", Addr: "127.0.0.1:0", Signals: logsSignals(), Processor: blockingProcessor(entered, release)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = postURL(t, "http://"+srv.Addr("a").String()+"/v1/logs", validLogs) }()
	<-entered
	cancel()
	err = waitDone(t, srv)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want DeadlineExceeded", err)
	}
	if n := strings.Count(err.Error(), "deadline exceeded"); n != 1 {
		t.Errorf("Wait reports the timeout %d times: %v", n, err)
	}
}

func TestStart_TwoEndpointsTwoPorts(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{} // endpoint name -> host the request arrived on
	proc := func(_ context.Context, req otlp.Request, r []store.NewTelemetryRecord) ([]store.NewTelemetryRecord, error) {
		mu.Lock()
		seen[req.Endpoint] = req.Host
		mu.Unlock()
		return r, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv, err := otlp.Start(ctx, otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{
		{Name: "a", Addr: freeAddr(t), Signals: logsSignals(), Processor: proc},
		{Name: "b", Addr: freeAddr(t), Signals: []store.Signal{store.SignalMetrics}, Processor: proc},
	}})
	if err != nil {
		t.Fatal(err)
	}
	a, b := srv.Addr("a").String(), srv.Addr("b").String()
	if a == b {
		t.Fatalf("want separate listeners, both on %s", a)
	}
	if code, _ := postURL(t, "http://"+a+"/v1/logs", validLogs); code != http.StatusOK {
		t.Errorf("a logs: %d", code)
	}
	if code, _ := postURL(t, "http://"+b+"/v1/metrics", validMetrics); code != http.StatusOK {
		t.Errorf("b metrics: %d", code)
	}
	if code, _ := postURL(t, "http://"+a+"/v1/metrics", validMetrics); code != http.StatusNotFound {
		t.Errorf("b's signal on a's port: %d, want 404", code)
	}
	if seen["a"] != a || seen["b"] != b {
		t.Errorf("seen = %v", seen)
	}
	cancel()
	if err := waitDone(t, srv); err != nil {
		t.Fatal(err)
	}
}

func TestStart_TwoEndpointsOnePort(t *testing.T) {
	var mu sync.Mutex
	var names []string
	proc := func(_ context.Context, req otlp.Request, r []store.NewTelemetryRecord) ([]store.NewTelemetryRecord, error) {
		mu.Lock()
		names = append(names, req.Endpoint)
		mu.Unlock()
		return r, nil
	}
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv, err := otlp.Start(ctx, otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{
		{Name: "a", Addr: addr, BasePath: "/a", Signals: logsSignals(), Processor: proc},
		{Name: "b", Addr: addr, BasePath: "/b", Signals: logsSignals(), Processor: proc},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if srv.Addr("a").String() != srv.Addr("b").String() {
		t.Fatal("want one shared listener")
	}
	for _, p := range []string{"/a/logs", "/b/logs"} {
		if code, _ := postURL(t, "http://"+addr+p, validLogs); code != http.StatusOK {
			t.Errorf("%s: %d", p, code)
		}
	}
	if strings.Join(names, ",") != "a,b" {
		t.Errorf("endpoint names = %v, want a,b", names)
	}
	cancel()
	if err := waitDone(t, srv); err != nil {
		t.Fatal(err)
	}
}

func TestStart_ReadHeaderTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv, err := otlp.Start(ctx, otlp.Config{
		Store:             &fakeStore{},
		ReadHeaderTimeout: 50 * time.Millisecond,
		Endpoints:         []otlp.Endpoint{{Name: "a", Addr: "127.0.0.1:0", Signals: logsSignals()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", srv.Addr("a").String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("want server to close the idle connection (EOF), got %v", err)
	}
}

// startLogs starts one logs endpoint named name under basePath on addr.
func startLogs(ctx context.Context, t *testing.T, addr, name, basePath string, proc otlp.Processor) *otlp.Server {
	t.Helper()
	srv, err := otlp.Start(ctx, otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{
		{Name: name, Addr: addr, BasePath: basePath, Signals: logsSignals(), Processor: proc},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestStart_SeparateCallsShareAddr(t *testing.T) {
	addr := freeAddr(t)
	ctxA, cancelA := context.WithCancel(t.Context())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(t.Context())
	defer cancelB()
	a := startLogs(ctxA, t, addr, "a", "/a", nil)
	b := startLogs(ctxB, t, addr, "b", "/b", nil)
	if a.Addr("a").String() != b.Addr("b").String() {
		t.Fatalf("want one shared listener, got %s and %s", a.Addr("a"), b.Addr("b"))
	}
	for _, p := range []string{"/a/logs", "/b/logs"} {
		if code, _ := postURL(t, "http://"+addr+p, validLogs); code != http.StatusOK {
			t.Errorf("%s: %d", p, code)
		}
	}

	cancelA()
	if err := waitDone(t, a); err != nil {
		t.Fatalf("Wait a = %v", err)
	}
	if code, _ := postURL(t, "http://"+addr+"/a/logs", validLogs); code != http.StatusNotFound {
		t.Errorf("/a/logs after a stopped: %d, want 404", code)
	}
	if code, _ := postURL(t, "http://"+addr+"/b/logs", validLogs); code != http.StatusOK {
		t.Errorf("/b/logs after a stopped: %d, want 200", code)
	}

	cancelB()
	if err := waitDone(t, b); err != nil {
		t.Fatalf("Wait b = %v", err)
	}
	assertClosed(t, addr)
}

func TestStart_SeparateCallsPortZeroNotShared(t *testing.T) {
	a := startLogs(t.Context(), t, "127.0.0.1:0", "a", "/a", nil)
	b := startLogs(t.Context(), t, "127.0.0.1:0", "b", "/b", nil)
	if a.Addr("a").String() == b.Addr("b").String() {
		t.Fatalf("port 0 must bind a new port per call, both on %s", a.Addr("a"))
	}
}

func TestStart_SeparateCallsConflicts(t *testing.T) {
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(t.Context())
	first := startLogs(ctx, t, addr, "a", "", nil)
	for name, cfg := range map[string]otlp.Config{
		"same path": {Store: &fakeStore{}, Endpoints: []otlp.Endpoint{
			{Name: "b", Addr: addr, Signals: logsSignals()},
		}},
		"different timeouts": {Store: &fakeStore{}, ReadTimeout: time.Second, Endpoints: []otlp.Endpoint{
			{Name: "b", Addr: addr, BasePath: "/b", Signals: logsSignals()},
		}},
	} {
		srv, err := otlp.Start(t.Context(), cfg)
		if !errors.Is(err, otlp.ErrInvalidConfig) || srv != nil {
			t.Errorf("%s: want ErrInvalidConfig and nil server, got %v, %v", name, srv, err)
		}
	}
	if code, _ := postURL(t, "http://"+addr+"/v1/logs", validLogs); code != http.StatusOK {
		t.Errorf("first call broken by rejected second call: %d", code)
	}
	cancel()
	if err := waitDone(t, first); err != nil {
		t.Fatal(err)
	}
	assertClosed(t, addr)
}

// Stopping one call on a shared address drains its own in-flight requests
// and leaves the other call serving.
func TestStart_SharedAddrDrainsInFlight(t *testing.T) {
	addr := freeAddr(t)
	entered, release := make(chan struct{}), make(chan struct{})
	ctxA, cancelA := context.WithCancel(t.Context())
	a := startLogs(ctxA, t, addr, "a", "/a", blockingProcessor(entered, release))
	startLogs(t.Context(), t, addr, "b", "/b", nil)

	result := make(chan int, 1)
	go func() {
		code, _ := postURL(t, "http://"+addr+"/a/logs", validLogs)
		result <- code
	}()
	<-entered
	cancelA()
	waited := make(chan error, 1)
	go func() { waited <- a.Wait() }()
	select {
	case err := <-waited:
		t.Fatalf("Wait returned %v while a request was in flight", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)

	if code := <-result; code != http.StatusOK {
		t.Errorf("in-flight request got %d, want 200", code)
	}
	if err := <-waited; err != nil {
		t.Fatalf("Wait a = %v", err)
	}
	if code, _ := postURL(t, "http://"+addr+"/b/logs", validLogs); code != http.StatusOK {
		t.Errorf("/b/logs after a stopped: %d", code)
	}
}

// A drain timeout on a shared address cancels the contexts of the requests
// left running.
func TestStart_SharedAddrDrainTimeoutCancelsRequests(t *testing.T) {
	addr := freeAddr(t)
	entered := make(chan struct{})
	requestErr := make(chan error, 1)
	proc := func(ctx context.Context, _ otlp.Request, r []store.NewTelemetryRecord) ([]store.NewTelemetryRecord, error) {
		close(entered)
		select {
		case <-ctx.Done():
			requestErr <- ctx.Err()
		case <-time.After(5 * time.Second):
			requestErr <- nil
		}
		return r, nil
	}
	ctxA, cancelA := context.WithCancel(t.Context())
	a, err := otlp.Start(ctxA, otlp.Config{Store: &fakeStore{}, ShutdownTimeout: 50 * time.Millisecond, Endpoints: []otlp.Endpoint{
		{Name: "a", Addr: addr, BasePath: "/a", Signals: logsSignals(), Processor: proc},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otlp.Start(t.Context(), otlp.Config{Store: &fakeStore{}, ShutdownTimeout: 50 * time.Millisecond, Endpoints: []otlp.Endpoint{
		{Name: "b", Addr: addr, BasePath: "/b", Signals: logsSignals()},
	}}); err != nil {
		t.Fatal(err)
	}

	go func() { _, _ = postURL(t, "http://"+addr+"/a/logs", validLogs) }()
	<-entered
	cancelA()
	if err := waitDone(t, a); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait a = %v, want DeadlineExceeded", err)
	}
	if err := <-requestErr; err == nil {
		t.Error("request context still live after Wait returned")
	}
}

// The last call on an address releases the port at once, not after the
// drain, so a restart on the same address can bind it.
func TestStart_RestartWhileDraining(t *testing.T) {
	addr := freeAddr(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	ctxA, cancelA := context.WithCancel(t.Context())
	startLogs(ctxA, t, addr, "a", "", blockingProcessor(entered, release))
	go func() { _, _ = postURL(t, "http://"+addr+"/v1/logs", validLogs) }()
	<-entered
	cancelA()

	deadline := time.Now().Add(time.Second)
	for {
		_, err := otlp.Start(t.Context(), otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{
			{Name: "b", Addr: addr, Signals: logsSignals()},
		}})
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart while a drains: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
