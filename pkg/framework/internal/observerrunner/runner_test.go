package observerrunner_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/internal/observerrunner"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/store/memstore"
)

// safeBuf is an io.Writer whose Write and String methods are mutually
// exclusive, so a test goroutine may read what a runner goroutine logged
// without racing on the underlying buffer.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// ---------------------------------------------------------------------------
// Spy observer
// ---------------------------------------------------------------------------

type spyObserver struct {
	name     string
	filter   store.TelemetryFilter
	called   chan store.TelemetryRecord
	onDetect func(context.Context, store.ObserverStore)
	retErr   error
}

func newSpyObs(name string, f store.TelemetryFilter) *spyObserver {
	return &spyObserver{name: name, filter: f, called: make(chan store.TelemetryRecord, 16)}
}

func (o *spyObserver) Name() string                  { return o.name }
func (o *spyObserver) Filter() store.TelemetryFilter { return o.filter }
func (o *spyObserver) Detect(ctx context.Context, _ *slog.Logger, r store.TelemetryRecord, s store.ObserverStore) error {
	if o.onDetect != nil {
		o.onDetect(ctx, s)
	}
	o.called <- r
	return o.retErr
}

// ---------------------------------------------------------------------------
// Spy store — controls the telemetry channel and captures AddInsight calls.
// ---------------------------------------------------------------------------

type spyStore struct {
	telCh    chan store.TelemetryRecord
	subErr   error
	insights []store.Insight
}

func newSpyStore() *spyStore {
	return &spyStore{telCh: make(chan store.TelemetryRecord, 16)}
}

func (s *spyStore) SubscribeTelemetry(_ context.Context, _ store.TelemetryFilter) (<-chan store.TelemetryRecord, error) {
	return s.telCh, s.subErr
}
func (s *spyStore) AddInsight(_ context.Context, i store.Insight) error {
	s.insights = append(s.insights, i)
	return nil
}

// Stub implementations satisfy store.Store.
func (s *spyStore) AddTelemetry(context.Context, []store.TelemetryRecord) error {
	return nil
}
func (s *spyStore) SubscribeInsights(context.Context, store.InsightFilter) (<-chan store.Insight, error) {
	return nil, nil
}
func (s *spyStore) AddActionLog(context.Context, store.ActionLog) error           { return nil }
func (s *spyStore) AddRecordedAction(context.Context, store.RecordedAction) error { return nil }
func (s *spyStore) QueryTelemetryByTime(context.Context, store.TelemetryTimeRangeQuery) (store.QueryResult[store.TelemetryRecord], error) {
	return store.QueryResult[store.TelemetryRecord]{}, nil
}
func (s *spyStore) QueryTelemetryFromCursor(context.Context, store.TelemetryCursorQuery) (store.QueryResult[store.TelemetryRecord], error) {
	return store.QueryResult[store.TelemetryRecord]{}, nil
}
func (s *spyStore) Close() {}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func startRunner(t *testing.T, run func(context.Context) error) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	return cancel, done
}

func awaitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop in time")
		return nil
	}
}

func awaitDetect(t *testing.T, ch <-chan store.TelemetryRecord) store.TelemetryRecord {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Detect call")
		return store.TelemetryRecord{}
	}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNew_PanicsOnNilStore(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for nil store")
		}
	}()
	observerrunner.New(nil, newSpyObs("o", store.TelemetryFilter{}))
}

func TestNew_PanicsOnNilObserver(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for nil observer")
		}
	}()
	observerrunner.New(newSpyStore(), nil)
}

// ---------------------------------------------------------------------------
// Run lifecycle
// ---------------------------------------------------------------------------

func TestRun_SubscribeError(t *testing.T) {
	st := newSpyStore()
	st.subErr = store.ErrStoreClosed
	runner := observerrunner.New(st, newSpyObs("o", store.TelemetryFilter{}))

	err := runner.Run(context.Background())
	if !errors.Is(err, store.ErrStoreClosed) {
		t.Errorf("want ErrStoreClosed, got %v", err)
	}
}

func TestRun_CancelContext(t *testing.T) {
	runner := observerrunner.New(newSpyStore(), newSpyObs("o", store.TelemetryFilter{}))
	cancel, done := startRunner(t, runner.Run)
	cancel()
	if err := awaitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestRun_ChannelClose_ReturnsNil(t *testing.T) {
	st := newSpyStore()
	runner := observerrunner.New(st, newSpyObs("o", store.TelemetryFilter{}))
	_, done := startRunner(t, runner.Run)
	close(st.telCh)
	if err := awaitDone(t, done); err != nil {
		t.Errorf("want nil on channel close, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Dispatch and filtering
// ---------------------------------------------------------------------------

func TestRun_DetectCalledWithRecord(t *testing.T) {
	st := newSpyStore()
	obs := newSpyObs("o", store.TelemetryFilter{})
	cancel, _ := startRunner(t, observerrunner.New(st, obs).Run)
	defer cancel()

	want := store.TelemetryRecord{ID: "test-id", Signal: store.SignalMetrics}
	st.telCh <- want

	got := awaitDetect(t, obs.called)
	if got.ID != want.ID {
		t.Errorf("Detect got ID %s, want %s", got.ID, want.ID)
	}
}

func TestRun_DetectError_ContinuesProcessing(t *testing.T) {
	st := newSpyStore()
	obs := newSpyObs("o", store.TelemetryFilter{})
	obs.retErr = errors.New("detect failed")
	cancel, _ := startRunner(t, observerrunner.New(st, obs).Run)
	defer cancel()

	st.telCh <- store.TelemetryRecord{Signal: store.SignalMetrics}
	awaitDetect(t, obs.called)
	st.telCh <- store.TelemetryRecord{Signal: store.SignalLogs}
	awaitDetect(t, obs.called) // second record still delivered despite first error
}

// telSubNotifier wraps a memstore and signals once when SubscribeTelemetry is
// called, so the test can wait until the runner has subscribed before adding
// records (avoids a subscription-timing race).
type telSubNotifier struct {
	*memstore.Store
	subscribed chan struct{}
}

func (s *telSubNotifier) SubscribeTelemetry(ctx context.Context, f store.TelemetryFilter) (<-chan store.TelemetryRecord, error) {
	select {
	case s.subscribed <- struct{}{}:
	default:
	}
	return s.Store.SubscribeTelemetry(ctx, f)
}

func TestRun_FilterRespected(t *testing.T) {
	ns := &telSubNotifier{Store: memstore.New(), subscribed: make(chan struct{}, 1)}
	obs := newSpyObs("o", store.TelemetryFilter{Signal: store.SignalMetrics})
	cancel, _ := startRunner(t, observerrunner.New(ns, obs).Run)
	defer cancel()

	select {
	case <-ns.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not subscribe in time")
	}

	ctx := context.Background()
	if err := ns.AddTelemetry(ctx, []store.TelemetryRecord{{ID: store.NewID(), Signal: store.SignalLogs}}); err != nil {
		t.Fatal(err)
	}
	if err := ns.AddTelemetry(ctx, []store.TelemetryRecord{{ID: store.NewID(), Signal: store.SignalMetrics}}); err != nil {
		t.Fatal(err)
	}

	got := awaitDetect(t, obs.called)
	if got.Signal != store.SignalMetrics {
		t.Errorf("Detect got signal %s, want metrics", got.Signal)
	}
	select {
	case extra := <-obs.called:
		t.Errorf("unexpected second Detect call: signal=%s", extra.Signal)
	case <-time.After(20 * time.Millisecond):
	}
}

// ---------------------------------------------------------------------------
// ObserverStore wrapper
// ---------------------------------------------------------------------------

// TestStoreWrapper_AddInsight_CreatedAtFromID verifies that CreatedAt is derived from the
// UUIDv7 ID, not from a separate time.Now() call.
func TestStoreWrapper_AddInsight_CreatedAtFromID(t *testing.T) {
	// This test ensures that the timestamp in CreatedAt and the one embedded
	// in the record ID are exactly the same and not a few microseconds off.
	const fixedID = store.RecordID("018cc251-f400-7abc-8def-123456789abc")
	fixedTime := time.UnixMilli(1704067200000).UTC() // 2024-01-01T00:00:00.000Z

	spy := newSpyStore()
	w := observerrunner.NewStoreWrapperWithIDGenerator(spy, "obs", func() store.RecordID { return fixedID })

	if _, err := w.AddInsight(context.Background(), store.NewInsight{Pattern: "p", Severity: store.SeverityInfo}); err != nil {
		t.Fatal(err)
	}
	if len(spy.insights) != 1 {
		t.Fatalf("want 1 insight, got %d", len(spy.insights))
	}
	if !spy.insights[0].CreatedAt.Equal(fixedTime) {
		t.Errorf("CreatedAt %v does not match fixedTime %v", spy.insights[0].CreatedAt, fixedTime)
	}
}

// TestRun_AddInsight_InjectsObserverName verifies that the storeWrapper injects
// the observer's name into every insight it creates.
func TestRun_AddInsight_InjectsObserverName(t *testing.T) {
	const name = "my-observer"
	st := newSpyStore()
	obs := newSpyObs(name, store.TelemetryFilter{})
	// onDetect runs synchronously inside Detect, before obs.called receives.
	// After awaitDetect returns, the channel send has completed, so the insight
	// write is visible to this goroutine (Go channel happens-before guarantee).
	obs.onDetect = func(ctx context.Context, s store.ObserverStore) {
		if _, err := s.AddInsight(ctx, store.NewInsight{Pattern: "p", Severity: store.SeverityInfo}); err != nil {
			t.Errorf("AddInsight: %v", err)
		}
	}

	cancel, _ := startRunner(t, observerrunner.New(st, obs).Run)
	defer cancel()

	st.telCh <- store.TelemetryRecord{Signal: store.SignalMetrics}
	awaitDetect(t, obs.called)

	if len(st.insights) != 1 {
		t.Fatalf("want 1 insight, got %d", len(st.insights))
	}
	if st.insights[0].ObserverName != name {
		t.Errorf("ObserverName: got %q, want %q", st.insights[0].ObserverName, name)
	}
}

// TestRun_AddInsight_LogsAtInfo verifies the observer runner emits an Info log
// — scoped with component and name — when an observer records an insight.
func TestRun_AddInsight_LogsAtInfo(t *testing.T) {
	const name = "my-observer"
	var sb safeBuf
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&sb, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })

	st := newSpyStore()
	obs := newSpyObs(name, store.TelemetryFilter{})
	obs.onDetect = func(ctx context.Context, s store.ObserverStore) {
		if _, err := s.AddInsight(ctx, store.NewInsight{Pattern: "high-cpu", Severity: store.SeverityCritical}); err != nil {
			t.Errorf("AddInsight: %v", err)
		}
	}

	cancel, _ := startRunner(t, observerrunner.New(st, obs).Run)
	defer cancel()

	st.telCh <- store.TelemetryRecord{Signal: store.SignalMetrics}
	awaitDetect(t, obs.called)

	out := sb.String()
	for _, want := range []string{`"msg":"Insight written"`, `"component":"observer"`, `"name":"` + name + `"`, `"pattern":"high-cpu"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %s\ngot: %s", want, out)
		}
	}
}
