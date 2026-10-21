package satellite_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.tools.sap/autonomous-operations/aos-framework/pkg/framework/registry"
	"github.tools.sap/autonomous-operations/aos-framework/pkg/framework/satellite"
	"github.tools.sap/autonomous-operations/aos-framework/pkg/framework/store"
	"github.tools.sap/autonomous-operations/aos-framework/pkg/store/memstore"
)

// ---------------------------------------------------------------------------
// Fakes — collector
// ---------------------------------------------------------------------------

type setupCall struct {
	ctx context.Context
	st  store.CollectorStore
}

type spyCollector struct {
	name  string
	setup chan setupCall
}

func newSpyCollector(name string) *spyCollector {
	return &spyCollector{name: name, setup: make(chan setupCall, 1)}
}

func (c *spyCollector) Name() string { return c.name }
func (c *spyCollector) Setup(ctx context.Context, _ *slog.Logger, st store.CollectorStore) {
	c.setup <- setupCall{ctx, st}
}

// ---------------------------------------------------------------------------
// Fakes — observer / operator (minimal noops)
// ---------------------------------------------------------------------------

type spyObserver struct{ name string }

func (o *spyObserver) Name() string                  { return o.name }
func (o *spyObserver) Filter() store.TelemetryFilter { return store.TelemetryFilter{} }
func (o *spyObserver) Detect(_ context.Context, _ *slog.Logger, _ store.TelemetryRecord, _ store.ObserverStore) error {
	return nil
}

type spyOperator struct{ name string }

func (o *spyOperator) Name() string                { return o.name }
func (o *spyOperator) Filter() store.InsightFilter { return store.InsightFilter{} }
func (o *spyOperator) Execute(_ context.Context, _ *slog.Logger, _ store.Insight, _ store.OperatorStore) error {
	return nil
}

// ---------------------------------------------------------------------------
// Spy logger
// ---------------------------------------------------------------------------

type spyHandler struct {
	mu      sync.Mutex
	records []slog.Record
	signal  chan struct{} // receives once per Error-level record
}

func newSpyHandler() *spyHandler {
	return &spyHandler{signal: make(chan struct{}, 8)}
}

func (h *spyHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *spyHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r)
	h.mu.Unlock()
	if r.Level >= slog.LevelError {
		select {
		case h.signal <- struct{}{}:
		default:
		}
	}
	return nil
}

func (h *spyHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *spyHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *spyHandler) errorCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.Level >= slog.LevelError {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// notifyStore — wraps memstore and signals on each subscribe call.
// The channels are buffered at 16 so multiple runners can signal without
// blocking, covering tests that register several observers or operators.
// ---------------------------------------------------------------------------

type notifyStore struct {
	*memstore.Store
	telSub chan struct{}
	insSub chan struct{}
}

var _ store.Store = (*notifyStore)(nil)

func newNotifyStore() *notifyStore {
	return &notifyStore{
		Store:  memstore.New(),
		telSub: make(chan struct{}, 16),
		insSub: make(chan struct{}, 16),
	}
}

func (s *notifyStore) SubscribeTelemetry(ctx context.Context, f store.TelemetryFilter) (<-chan store.TelemetryRecord, error) {
	s.telSub <- struct{}{}
	return s.Store.SubscribeTelemetry(ctx, f)
}

func (s *notifyStore) SubscribeInsights(ctx context.Context, f store.InsightFilter) (<-chan store.Insight, error) {
	s.insSub <- struct{}{}
	return s.Store.SubscribeInsights(ctx, f)
}

// ---------------------------------------------------------------------------
// Error stores — return ErrStoreClosed from one subscribe method each.
// ---------------------------------------------------------------------------

type errTelStore struct{ *memstore.Store }

var _ store.Store = (*errTelStore)(nil)

func (s *errTelStore) SubscribeTelemetry(_ context.Context, _ store.TelemetryFilter) (<-chan store.TelemetryRecord, error) {
	return nil, store.ErrStoreClosed
}

type errInsightStore struct{ *memstore.Store }

var _ store.Store = (*errInsightStore)(nil)

func (s *errInsightStore) SubscribeInsights(_ context.Context, _ store.InsightFilter) (<-chan store.Insight, error) {
	return nil, store.ErrStoreClosed
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func startSatellite(t *testing.T, cfg satellite.Config) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- satellite.Run(ctx, cfg) }()
	return cancel, done
}

func awaitReturn(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("satellite.Run did not return in time")
		return nil
	}
}

func awaitSetup(t *testing.T, ch <-chan setupCall) setupCall {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Setup call")
		return setupCall{}
	}
}

func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// ---------------------------------------------------------------------------
// blockingStore — wraps memstore. Each subscribe method closes a signal
// channel (once) as soon as it is entered, then blocks until the test
// releases it. This lets tests assert startup ordering without relying on
// timing.
// ---------------------------------------------------------------------------

type blockingStore struct {
	*memstore.Store
	telOnce    sync.Once
	telEntered chan struct{}
	telRelease chan struct{}
	insOnce    sync.Once
	insEntered chan struct{}
	insRelease chan struct{}
}

func newBlockingStore() *blockingStore {
	return &blockingStore{
		Store:      memstore.New(),
		telEntered: make(chan struct{}),
		telRelease: make(chan struct{}),
		insEntered: make(chan struct{}),
		insRelease: make(chan struct{}),
	}
}

func (s *blockingStore) SubscribeTelemetry(ctx context.Context, f store.TelemetryFilter) (<-chan store.TelemetryRecord, error) {
	s.telOnce.Do(func() { close(s.telEntered) })
	select {
	case <-s.telRelease:
		return s.Store.SubscribeTelemetry(ctx, f)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *blockingStore) SubscribeInsights(ctx context.Context, f store.InsightFilter) (<-chan store.Insight, error) {
	s.insOnce.Do(func() { close(s.insEntered) })
	select {
	case <-s.insRelease:
		return s.Store.SubscribeInsights(ctx, f)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// Startup ordering
// ---------------------------------------------------------------------------

// TestRun_CollectorSetup_WaitsForObserverSubscription verifies that collector
// Setup is not called until all observers have subscribed. The blockingStore
// holds SubscribeTelemetry until the test releases it; the negative assertion
// is structurally guaranteed because the observer goroutine is parked inside
// that call and cannot have advanced to the ready signal that gates Setup.
func TestRun_CollectorSetup_WaitsForObserverSubscription(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	col := newSpyCollector("c")
	bs := newBlockingStore()
	registry.RegisterCollector(col)
	registry.RegisterObserver(&spyObserver{"obs"})

	cancel, _ := startSatellite(t, satellite.Config{Store: bs})
	t.Cleanup(cancel)

	awaitSignal(t, bs.telEntered, "SubscribeTelemetry entered")

	select {
	case <-col.setup:
		t.Fatal("collector Setup called before observer subscribed")
	default:
	}

	close(bs.telRelease)
	awaitSetup(t, col.setup)
}

// TestRun_ObserverStart_WaitsForOperatorSubscription verifies that observer
// goroutines are not started until all operators have subscribed.
func TestRun_ObserverStart_WaitsForOperatorSubscription(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	bs := newBlockingStore()
	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterObserver(&spyObserver{"obs"})
	registry.RegisterOperator(&spyOperator{"op"})

	cancel, _ := startSatellite(t, satellite.Config{Store: bs})
	t.Cleanup(cancel)

	awaitSignal(t, bs.insEntered, "SubscribeInsights entered")

	select {
	case <-bs.telEntered:
		t.Fatal("observer subscribed before operator subscribed")
	default:
	}

	close(bs.insRelease)
	awaitSignal(t, bs.telEntered, "SubscribeTelemetry entered")
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func TestRun_ReturnsNilOnContextCancel(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	col := newSpyCollector("c")
	registry.RegisterCollector(col)

	cancel, done := startSatellite(t, satellite.Config{Store: memstore.New()})
	awaitSetup(t, col.setup)
	cancel()
	if err := awaitReturn(t, done); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

func TestRun_EmptyRegistry_ReturnsErrNoCollectors(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	_, done := startSatellite(t, satellite.Config{})
	if err := awaitReturn(t, done); !errors.Is(err, satellite.ErrNoCollectors) {
		t.Errorf("want ErrNoCollectors, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Startup invariants
// ---------------------------------------------------------------------------

func TestRun_NoCollectors_ReturnsError(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	_, done := startSatellite(t, satellite.Config{})
	if err := awaitReturn(t, done); !errors.Is(err, satellite.ErrNoCollectors) {
		t.Errorf("want ErrNoCollectors, got %v", err)
	}
}

func TestRun_OperatorsWithoutObservers_ReturnsError(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterOperator(&spyOperator{"op"})

	_, done := startSatellite(t, satellite.Config{})
	if err := awaitReturn(t, done); !errors.Is(err, satellite.ErrOperatorsWithoutObservers) {
		t.Errorf("want ErrOperatorsWithoutObservers, got %v", err)
	}
}

func TestRun_EmptyCollectorName_ReturnsError(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	registry.RegisterCollector(&spyCollector{name: ""})

	_, done := startSatellite(t, satellite.Config{})
	if err := awaitReturn(t, done); !errors.Is(err, satellite.ErrEmptyName) {
		t.Errorf("want ErrEmptyName, got %v", err)
	}
}

func TestRun_EmptyObserverName_ReturnsError(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterObserver(&spyObserver{name: ""})

	_, done := startSatellite(t, satellite.Config{})
	if err := awaitReturn(t, done); !errors.Is(err, satellite.ErrEmptyName) {
		t.Errorf("want ErrEmptyName, got %v", err)
	}
}

func TestRun_EmptyOperatorName_ReturnsError(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterObserver(&spyObserver{"obs"})
	registry.RegisterOperator(&spyOperator{name: ""})

	_, done := startSatellite(t, satellite.Config{})
	if err := awaitReturn(t, done); !errors.Is(err, satellite.ErrEmptyName) {
		t.Errorf("want ErrEmptyName, got %v", err)
	}
}

func TestRun_DuplicateCollectorNames_ReturnsError(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterCollector(newSpyCollector("c"))

	_, done := startSatellite(t, satellite.Config{})
	if err := awaitReturn(t, done); !errors.Is(err, satellite.ErrDuplicateName) {
		t.Errorf("want ErrDuplicateName, got %v", err)
	}
}

func TestRun_DuplicateObserverNames_ReturnsError(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterObserver(&spyObserver{"obs"})
	registry.RegisterObserver(&spyObserver{"obs"})

	_, done := startSatellite(t, satellite.Config{})
	if err := awaitReturn(t, done); !errors.Is(err, satellite.ErrDuplicateName) {
		t.Errorf("want ErrDuplicateName, got %v", err)
	}
}

func TestRun_DuplicateOperatorNames_ReturnsError(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterObserver(&spyObserver{"obs"})
	registry.RegisterOperator(&spyOperator{"op"})
	registry.RegisterOperator(&spyOperator{"op"})

	_, done := startSatellite(t, satellite.Config{})
	if err := awaitReturn(t, done); !errors.Is(err, satellite.ErrDuplicateName) {
		t.Errorf("want ErrDuplicateName, got %v", err)
	}
}

func TestRun_CollectorsOnly_Starts(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	col := newSpyCollector("c")
	registry.RegisterCollector(col)

	cancel, done := startSatellite(t, satellite.Config{Store: memstore.New()})
	t.Cleanup(cancel)

	awaitSetup(t, col.setup)

	cancel()
	if err := awaitReturn(t, done); err != nil {
		t.Errorf("collectors-only satellite should start; got %v", err)
	}
}

func TestRun_CollectorsAndObserversOnly_Starts(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	col := newSpyCollector("c")
	ns := newNotifyStore()
	registry.RegisterCollector(col)
	registry.RegisterObserver(&spyObserver{"obs"})

	cancel, done := startSatellite(t, satellite.Config{Store: ns})
	t.Cleanup(cancel)

	awaitSetup(t, col.setup)
	awaitSignal(t, ns.telSub, "SubscribeTelemetry")

	cancel()
	if err := awaitReturn(t, done); err != nil {
		t.Errorf("collectors+observers satellite should start; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Store injection
// ---------------------------------------------------------------------------

func TestRun_NilStore_ReturnsError(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	cancel, done := startSatellite(t, satellite.Config{})
	t.Cleanup(cancel)
	if err := awaitReturn(t, done); err == nil {
		t.Error("want error when Config.Store is nil, got nil")
	}
}

func TestRun_InjectedStore_CollectorReceivesStore(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	col := newSpyCollector("c")
	registry.RegisterCollector(col)

	cancel, done := startSatellite(t, satellite.Config{Store: memstore.New()})

	call := awaitSetup(t, col.setup)
	cancel()
	if err := awaitReturn(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("satellite exited with error: %v", err)
	}

	if call.st == nil {
		t.Error("Setup received nil store; expected the injected store")
	}
}

func TestRun_NilLogger_DoesNotPanic(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	col := newSpyCollector("c")
	registry.RegisterCollector(col)

	cancel, done := startSatellite(t, satellite.Config{Store: memstore.New()})
	awaitSetup(t, col.setup)
	cancel()
	if err := awaitReturn(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("satellite exited with error: %v", err)
	} // must not panic
}

// ---------------------------------------------------------------------------
// Collector wiring
// ---------------------------------------------------------------------------

func TestRun_Collector_SetupCalledOnce(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	col := newSpyCollector("c")
	registry.RegisterCollector(col)

	cancel, _ := startSatellite(t, satellite.Config{Store: memstore.New()})
	t.Cleanup(cancel)

	awaitSetup(t, col.setup)

	select {
	case <-col.setup:
		t.Error("Setup called more than once")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestRun_Collector_AllCollectorsSetup(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	cols := []*spyCollector{
		newSpyCollector("c1"),
		newSpyCollector("c2"),
		newSpyCollector("c3"),
	}
	for _, c := range cols {
		registry.RegisterCollector(c)
	}

	cancel, _ := startSatellite(t, satellite.Config{Store: memstore.New()})
	t.Cleanup(cancel)

	for _, c := range cols {
		awaitSetup(t, c.setup)
	}
}

func TestRun_Collector_ContextPassedToSetup(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	col := newSpyCollector("c")
	registry.RegisterCollector(col)

	cancel, done := startSatellite(t, satellite.Config{Store: memstore.New()})

	call := awaitSetup(t, col.setup)
	cancel()
	if err := awaitReturn(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("satellite exited with error: %v", err)
	}

	select {
	case <-call.ctx.Done():
	default:
		t.Error("context passed to Setup is not Done after Run context cancel")
	}
}

// ---------------------------------------------------------------------------
// Observer wiring
// ---------------------------------------------------------------------------

func TestRun_Observer_RunnerStarted(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	ns := newNotifyStore()
	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterObserver(&spyObserver{"obs"})

	cancel, _ := startSatellite(t, satellite.Config{Store: ns})
	t.Cleanup(cancel)

	awaitSignal(t, ns.telSub, "SubscribeTelemetry")
}

func TestRun_Observer_AllRunnersStarted(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	ns := newNotifyStore()
	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterObserver(&spyObserver{"o1"})
	registry.RegisterObserver(&spyObserver{"o2"})
	registry.RegisterObserver(&spyObserver{"o3"})

	cancel, _ := startSatellite(t, satellite.Config{Store: ns})
	t.Cleanup(cancel)

	for range 3 {
		awaitSignal(t, ns.telSub, "SubscribeTelemetry")
	}
}

// ---------------------------------------------------------------------------
// Operator wiring
// ---------------------------------------------------------------------------

func TestRun_Operator_RunnerStarted(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	ns := newNotifyStore()
	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterObserver(&spyObserver{"obs"})
	registry.RegisterOperator(&spyOperator{"op"})

	cancel, _ := startSatellite(t, satellite.Config{Store: ns})
	t.Cleanup(cancel)

	awaitSignal(t, ns.insSub, "SubscribeInsights")
}

func TestRun_Operator_AllRunnersStarted(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	ns := newNotifyStore()
	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterObserver(&spyObserver{"obs"})
	registry.RegisterOperator(&spyOperator{"op1"})
	registry.RegisterOperator(&spyOperator{"op2"})
	registry.RegisterOperator(&spyOperator{"op3"})

	cancel, _ := startSatellite(t, satellite.Config{Store: ns})
	t.Cleanup(cancel)

	for range 3 {
		awaitSignal(t, ns.insSub, "SubscribeInsights")
	}
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

func TestRun_ObserverError_IsLogged(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	handler := newSpyHandler()
	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterObserver(&spyObserver{"obs"})

	cancel, _ := startSatellite(t, satellite.Config{
		Store:  &errTelStore{memstore.New()},
		Logger: slog.New(handler),
	})
	t.Cleanup(cancel)

	awaitSignal(t, handler.signal, "error log")

	if handler.errorCount() == 0 {
		t.Error("expected at least one Error-level log record")
	}
}

func TestRun_OperatorError_IsLogged(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	handler := newSpyHandler()
	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterObserver(&spyObserver{"obs"})
	registry.RegisterOperator(&spyOperator{"op"})

	cancel, _ := startSatellite(t, satellite.Config{
		Store:  &errInsightStore{memstore.New()},
		Logger: slog.New(handler),
	})
	t.Cleanup(cancel)

	awaitSignal(t, handler.signal, "error log")

	if handler.errorCount() == 0 {
		t.Error("expected at least one Error-level log record")
	}
}

// TestRun_ContextCanceled_NotLogged verifies that context.Canceled returned by
// a runner goroutine is swallowed and does not produce an error log entry.
func TestRun_ContextCanceled_NotLogged(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	handler := newSpyHandler()
	ns := newNotifyStore()
	registry.RegisterCollector(newSpyCollector("c"))
	registry.RegisterObserver(&spyObserver{"obs"})
	registry.RegisterOperator(&spyOperator{"op"})

	cancel, done := startSatellite(t, satellite.Config{
		Store:  ns,
		Logger: slog.New(handler),
	})

	// Wait for both runners to subscribe before cancelling so they are already
	// in their blocking select and will exit cleanly with context.Canceled.
	awaitSignal(t, ns.telSub, "SubscribeTelemetry")
	awaitSignal(t, ns.insSub, "SubscribeInsights")

	cancel()
	if err := awaitReturn(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("satellite exited with error: %v", err)
	}

	// Allow runner goroutines to finish before inspecting the log.
	time.Sleep(50 * time.Millisecond)

	if n := handler.errorCount(); n != 0 {
		t.Errorf("expected no Error-level logs on clean shutdown, got %d", n)
	}
}
