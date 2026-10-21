package operatorrunner_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/autonomous-operations/aos-framework/pkg/framework/internal/operatorrunner"
	"github.com/autonomous-operations/aos-framework/pkg/framework/store"
	"github.com/autonomous-operations/aos-framework/pkg/store/memstore"
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
// Spy operator
// ---------------------------------------------------------------------------

type executeCall struct {
	insight store.Insight
}

type spyOperator struct {
	name      string
	filter    store.InsightFilter
	called    chan executeCall
	retErr    error
	doPanic   bool
	onExecute func(context.Context, store.OperatorStore)
}

func newSpyOp(name string, f store.InsightFilter) *spyOperator {
	return &spyOperator{name: name, filter: f, called: make(chan executeCall, 16)}
}

func (o *spyOperator) Name() string                { return o.name }
func (o *spyOperator) Filter() store.InsightFilter { return o.filter }
func (o *spyOperator) Execute(ctx context.Context, _ *slog.Logger, ins store.Insight, s store.OperatorStore) error {
	if o.onExecute != nil {
		o.onExecute(ctx, s)
	}
	o.called <- executeCall{ins}
	if o.doPanic {
		panic("test panic")
	}
	return o.retErr
}

// loggingOperator calls AddActionLog once per execution, then signals on called.
type loggingOperator struct {
	name   string
	filter store.InsightFilter
	called chan executeCall
}

func (o *loggingOperator) Name() string                { return o.name }
func (o *loggingOperator) Filter() store.InsightFilter { return o.filter }
func (o *loggingOperator) Execute(ctx context.Context, _ *slog.Logger, ins store.Insight, s store.OperatorStore) error {
	_, _ = s.AddActionLog(ctx, store.NewActionLog{Description: "step", State: store.ActionLogStateInfo})
	o.called <- executeCall{ins}
	return nil
}

// blockingOperator enters Execute, signals on entered, then blocks until release is closed.
// This allows tests to inspect store state while the operator is still running.
type blockingOperator struct {
	name    string
	filter  store.InsightFilter
	entered chan struct{}
	release chan struct{}
}

func (o *blockingOperator) Name() string                { return o.name }
func (o *blockingOperator) Filter() store.InsightFilter { return o.filter }
func (o *blockingOperator) Execute(_ context.Context, _ *slog.Logger, _ store.Insight, _ store.OperatorStore) error {
	close(o.entered)
	<-o.release
	return nil
}

// ---------------------------------------------------------------------------
// Spy store — controls the insight channel and captures AddRecordedAction calls.
// ---------------------------------------------------------------------------

type spyStore struct {
	insightCh       chan store.Insight
	subErr          error
	recordedActions []store.RecordedAction
	actionLogs      []store.ActionLog
}

func newSpyStore() *spyStore {
	return &spyStore{insightCh: make(chan store.Insight, 16)}
}

func (s *spyStore) SubscribeInsights(_ context.Context, _ store.InsightFilter) (<-chan store.Insight, error) {
	return s.insightCh, s.subErr
}
func (s *spyStore) AddRecordedAction(_ context.Context, a store.RecordedAction) error {
	s.recordedActions = append(s.recordedActions, a)
	return nil
}

// Stub implementations satisfy store.Store.
func (s *spyStore) AddTelemetry(context.Context, []store.TelemetryRecord) error {
	return nil
}
func (s *spyStore) SubscribeTelemetry(context.Context, store.TelemetryFilter) (<-chan store.TelemetryRecord, error) {
	return nil, nil
}
func (s *spyStore) AddInsight(context.Context, store.Insight) error { return nil }
func (s *spyStore) AddActionLog(_ context.Context, al store.ActionLog) error {
	s.actionLogs = append(s.actionLogs, al)
	return nil
}
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

func awaitExecute(t *testing.T, ch <-chan executeCall) executeCall {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Execute call")
		return executeCall{}
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
	operatorrunner.New(nil, newSpyOp("op", store.InsightFilter{}))
}

func TestNew_PanicsOnNilOperator(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for nil operator")
		}
	}()
	operatorrunner.New(newSpyStore(), nil)
}

// ---------------------------------------------------------------------------
// Run lifecycle
// ---------------------------------------------------------------------------

func TestRun_SubscribeError(t *testing.T) {
	st := newSpyStore()
	st.subErr = store.ErrStoreClosed
	runner := operatorrunner.New(st, newSpyOp("op", store.InsightFilter{}))

	err := runner.Run(context.Background())
	if !errors.Is(err, store.ErrStoreClosed) {
		t.Errorf("want ErrStoreClosed, got %v", err)
	}
}

func TestRun_CancelContext(t *testing.T) {
	runner := operatorrunner.New(newSpyStore(), newSpyOp("op", store.InsightFilter{}))
	cancel, done := startRunner(t, runner.Run)
	cancel()
	if err := awaitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestRun_ChannelClose_ReturnsNil(t *testing.T) {
	st := newSpyStore()
	runner := operatorrunner.New(st, newSpyOp("op", store.InsightFilter{}))
	_, done := startRunner(t, runner.Run)
	close(st.insightCh)
	if err := awaitDone(t, done); err != nil {
		t.Errorf("want nil on channel close, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Dispatch and filtering
// ---------------------------------------------------------------------------

func TestRun_Execute_CalledWithCorrectArgs(t *testing.T) {
	st := newSpyStore()
	op := newSpyOp("op", store.InsightFilter{})
	cancel, _ := startRunner(t, operatorrunner.New(st, op).Run)
	defer cancel()

	ins := store.Insight{ID: "insight-1", Pattern: "high-error-rate", Severity: store.SeverityCritical}
	st.insightCh <- ins

	got := awaitExecute(t, op.called)
	if got.insight.ID != ins.ID || got.insight.Pattern != ins.Pattern {
		t.Errorf("insight mismatch: got %+v, want %+v", got.insight, ins)
	}
}

// insightSubNotifier wraps a memstore and signals once when SubscribeInsights is
// called, so the test can wait until the runner has subscribed before adding
// insights (avoids a subscription-timing race).
type insightSubNotifier struct {
	*memstore.Store
	subscribed chan struct{}
}

func (s *insightSubNotifier) SubscribeInsights(ctx context.Context, f store.InsightFilter) (<-chan store.Insight, error) {
	select {
	case s.subscribed <- struct{}{}:
	default:
	}
	return s.Store.SubscribeInsights(ctx, f)
}

func TestRun_FilterRespected(t *testing.T) {
	ns := &insightSubNotifier{Store: memstore.New(), subscribed: make(chan struct{}, 1)}
	op := newSpyOp("op", store.InsightFilter{Patterns: []string{"target"}})
	cancel, _ := startRunner(t, operatorrunner.New(ns, op).Run)
	defer cancel()

	select {
	case <-ns.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not subscribe in time")
	}

	ctx := context.Background()
	if err := ns.AddInsight(ctx, store.Insight{ID: store.NewID(), Pattern: "other", Severity: store.SeverityInfo}); err != nil {
		t.Fatal(err)
	}
	if err := ns.AddInsight(ctx, store.Insight{ID: store.NewID(), Pattern: "target", Severity: store.SeverityInfo}); err != nil {
		t.Fatal(err)
	}

	got := awaitExecute(t, op.called)
	if got.insight.Pattern != "target" {
		t.Errorf("Execute got pattern %q, want target", got.insight.Pattern)
	}
	select {
	case extra := <-op.called:
		t.Errorf("unexpected second Execute call: pattern=%s", extra.insight.Pattern)
	case <-time.After(20 * time.Millisecond):
	}
}

// ---------------------------------------------------------------------------
// RecordedAction tracking
//
// Pattern: send insight → await Execute (synchronization point) → cancel →
// await Run exit → inspect recordedActions.
// Run exit is the final happens-before: everything the runner goroutine wrote
// before sending on done is visible to this goroutine after receiving from done.
// ---------------------------------------------------------------------------

// TestRun_StartedRecordWrittenBeforeExecuteReturns verifies that a RecordedAction
// with StateStarted is written before Execute is called, and that a second record
// with the outcome state is written after Execute returns.
func TestRun_StartedRecordWrittenBeforeExecuteReturns(t *testing.T) {
	st := newSpyStore()
	op := &blockingOperator{
		name:    "op",
		filter:  store.InsightFilter{},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	cancel, done := startRunner(t, operatorrunner.New(st, op).Run)

	ins := store.Insight{ID: "i1", Pattern: "p", Severity: store.SeverityInfo}
	st.insightCh <- ins

	select {
	case <-op.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Execute to start")
	}

	// Execute is blocking; the started record must already be written.
	if len(st.recordedActions) != 1 {
		t.Fatalf("want 1 started record while operator is running, got %d", len(st.recordedActions))
	}
	ra := st.recordedActions[0]
	if ra.State != store.StateStarted {
		t.Errorf("State: got %s, want started", ra.State)
	}
	if ra.InsightID != ins.ID {
		t.Errorf("InsightID: got %s, want %s", ra.InsightID, ins.ID)
	}
	if ra.OperatorName != op.name {
		t.Errorf("OperatorName: got %s, want %s", ra.OperatorName, op.name)
	}
	if ra.ExecutionID == "" {
		t.Error("ExecutionID must not be empty")
	}
	if ra.Timestamp.IsZero() {
		t.Error("Timestamp must not be zero")
	}

	close(op.release)
	cancel()
	if err := awaitDone(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runner exited with error: %v", err)
	}

	if len(st.recordedActions) != 2 {
		t.Fatalf("want 2 records after completion, got %d", len(st.recordedActions))
	}
	if st.recordedActions[1].State != store.StateSuccess {
		t.Errorf("completion State: got %s, want success", st.recordedActions[1].State)
	}
	if st.recordedActions[1].ExecutionID != ra.ExecutionID {
		t.Errorf("ExecutionID mismatch: started %s, completion %s", ra.ExecutionID, st.recordedActions[1].ExecutionID)
	}
}

func TestRun_SuccessWritesRecordedAction(t *testing.T) {
	st := newSpyStore()
	op := newSpyOp("op", store.InsightFilter{})
	cancel, done := startRunner(t, operatorrunner.New(st, op).Run)

	ins := store.Insight{ID: "i1", Pattern: "p", Severity: store.SeverityInfo}
	st.insightCh <- ins
	awaitExecute(t, op.called)

	cancel()
	if err := awaitDone(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runner exited with error: %v", err)
	}

	if len(st.recordedActions) != 2 {
		t.Fatalf("want 2 recorded actions, got %d", len(st.recordedActions))
	}
	started := st.recordedActions[0]
	completed := st.recordedActions[1]

	if started.State != store.StateStarted {
		t.Errorf("started State: got %s, want started", started.State)
	}
	if completed.State != store.StateSuccess {
		t.Errorf("completed State: got %s, want success", completed.State)
	}
	if started.InsightID != ins.ID {
		t.Errorf("started InsightID: got %s, want %s", started.InsightID, ins.ID)
	}
	if completed.InsightID != ins.ID {
		t.Errorf("completed InsightID: got %s, want %s", completed.InsightID, ins.ID)
	}
	if started.OperatorName != op.name {
		t.Errorf("started OperatorName: got %s, want %s", started.OperatorName, op.name)
	}
	if completed.OperatorName != op.name {
		t.Errorf("completed OperatorName: got %s, want %s", completed.OperatorName, op.name)
	}
	if started.ExecutionID == "" {
		t.Error("started ExecutionID must not be empty")
	}
	if started.ExecutionID != completed.ExecutionID {
		t.Errorf("ExecutionID mismatch: started %s, completed %s", started.ExecutionID, completed.ExecutionID)
	}
	if started.Timestamp.IsZero() || completed.Timestamp.IsZero() {
		t.Error("Timestamp must not be zero for either record")
	}
}

func TestRun_FailureWritesRecordedAction(t *testing.T) {
	st := newSpyStore()
	op := newSpyOp("op", store.InsightFilter{})
	op.retErr = errors.New("execution failed")
	cancel, done := startRunner(t, operatorrunner.New(st, op).Run)

	st.insightCh <- store.Insight{Pattern: "p", Severity: store.SeverityInfo}
	awaitExecute(t, op.called)

	cancel()
	if err := awaitDone(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runner exited with error: %v", err)
	}

	if len(st.recordedActions) != 2 {
		t.Fatalf("want 2 recorded actions, got %d", len(st.recordedActions))
	}
	if st.recordedActions[0].State != store.StateStarted {
		t.Errorf("started State: got %s, want started", st.recordedActions[0].State)
	}
	if st.recordedActions[1].State != store.StateFailed {
		t.Errorf("completed State: got %s, want failed", st.recordedActions[1].State)
	}
	if st.recordedActions[0].ExecutionID != st.recordedActions[1].ExecutionID {
		t.Errorf("ExecutionID mismatch between started and failed records")
	}
}

func TestRun_PanicWritesRecordedAction(t *testing.T) {
	st := newSpyStore()
	op := newSpyOp("op", store.InsightFilter{})
	op.doPanic = true
	cancel, done := startRunner(t, operatorrunner.New(st, op).Run)

	// Execute sends on op.called before panicking; the recover in handleInsight
	// catches it and calls AddRecordedAction. Awaiting Run exit guarantees both
	// have completed.
	st.insightCh <- store.Insight{Pattern: "p", Severity: store.SeverityInfo}
	awaitExecute(t, op.called)

	cancel()
	if err := awaitDone(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runner exited with error: %v", err)
	}

	if len(st.recordedActions) != 2 {
		t.Fatalf("want 2 recorded actions after panic, got %d", len(st.recordedActions))
	}
	if st.recordedActions[0].State != store.StateStarted {
		t.Errorf("started State: got %s, want started", st.recordedActions[0].State)
	}
	if st.recordedActions[1].State != store.StateFailed {
		t.Errorf("completed State: got %s, want failed", st.recordedActions[1].State)
	}
	if st.recordedActions[0].ExecutionID != st.recordedActions[1].ExecutionID {
		t.Errorf("ExecutionID mismatch between started and panic-failed records")
	}
}

func TestRun_ActionLog_ExecutionIDMatchesRecordedAction(t *testing.T) {
	st := newSpyStore()
	op := &loggingOperator{
		name:   "op",
		filter: store.InsightFilter{},
		called: make(chan executeCall, 16),
	}
	cancel, done := startRunner(t, operatorrunner.New(st, op).Run)

	st.insightCh <- store.Insight{ID: "i1", Pattern: "p", Severity: store.SeverityInfo}
	awaitExecute(t, op.called)

	cancel()
	if err := awaitDone(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runner exited with error: %v", err)
	}

	if len(st.actionLogs) != 1 {
		t.Fatalf("want 1 action log, got %d", len(st.actionLogs))
	}
	if len(st.recordedActions) != 2 {
		t.Fatalf("want 2 recorded actions, got %d", len(st.recordedActions))
	}
	logExecID := st.actionLogs[0].ExecutionID
	if logExecID == "" {
		t.Error("ActionLog.ExecutionID must not be empty")
	}
	if logExecID != st.recordedActions[0].ExecutionID {
		t.Errorf("ExecutionID mismatch: ActionLog has %s, started RecordedAction has %s",
			logExecID, st.recordedActions[0].ExecutionID)
	}
	if logExecID != st.recordedActions[1].ExecutionID {
		t.Errorf("ExecutionID mismatch: ActionLog has %s, completed RecordedAction has %s",
			logExecID, st.recordedActions[1].ExecutionID)
	}
}

// ---------------------------------------------------------------------------
// ID-derived timestamps
// ---------------------------------------------------------------------------

// TestRun_RecordedAction_TimestampsFromID verifies that Timestamp on both the
// started and completed RecordedAction records is derived from the record's
// UUIDv7 ID, not from a separate time.Now() call.
func TestRun_RecordedAction_TimestampsFromID(t *testing.T) {
	const fixedID = store.RecordID("018cc251-f400-7abc-8def-123456789abc")
	fixedTime := time.UnixMilli(1704067200000).UTC() // 2024-01-01T00:00:00.000Z

	st := newSpyStore()
	op := newSpyOp("op", store.InsightFilter{})
	cancel, done := startRunner(t, operatorrunner.NewWithIDGenerator(st, op, func() store.RecordID { return fixedID }).Run)

	st.insightCh <- store.Insight{Pattern: "p", Severity: store.SeverityInfo}
	awaitExecute(t, op.called)
	cancel()
	if err := awaitDone(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runner exited with error: %v", err)
	}

	if len(st.recordedActions) != 2 {
		t.Fatalf("want 2 recorded actions, got %d", len(st.recordedActions))
	}
	for i, a := range st.recordedActions {
		if a.ID != fixedID {
			t.Errorf("record[%d] ID: got %v, want %v", i, a.ID, fixedID)
		}
		if !a.Timestamp.Equal(fixedTime) {
			t.Errorf("record[%d] Timestamp %v does not match fixedTime %v", i, a.Timestamp, fixedTime)
		}
	}
}

// TestStoreWrapper_AddActionLog_TimestampFromID verifies that Timestamp on an
// ActionLog is derived from the UUIDv7 ID, not from a separate time.Now() call.
func TestStoreWrapper_AddActionLog_TimestampFromID(t *testing.T) {
	// This test ensures that the timestamp in Timestamp and the one embedded
	// in the record ID are exactly the same and not a few microseconds off.
	const fixedID = store.RecordID("018cc251-f400-7abc-8def-123456789abc")
	fixedTime := time.UnixMilli(1704067200000).UTC() // 2024-01-01T00:00:00.000Z

	spy := newSpyStore()
	w := operatorrunner.NewStoreWrapperWithIDGenerator(spy, func() store.RecordID { return fixedID })

	if _, err := w.AddActionLog(context.Background(), store.NewActionLog{Description: "step", State: store.ActionLogStateInfo}); err != nil {
		t.Fatal(err)
	}
	if len(spy.actionLogs) != 1 {
		t.Fatalf("want 1 action log, got %d", len(spy.actionLogs))
	}
	if !spy.actionLogs[0].Timestamp.Equal(fixedTime) {
		t.Errorf("Timestamp %v does not match fixedTime %v", spy.actionLogs[0].Timestamp, fixedTime)
	}
}

// TestRun_AddActionLog_LogsAtInfo verifies the operator runner emits an Info log
// — scoped with component and name — when an operator records an action log.
func TestRun_AddActionLog_LogsAtInfo(t *testing.T) {
	const name = "my-operator"
	var sb safeBuf
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&sb, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })

	st := newSpyStore()
	op := newSpyOp(name, store.InsightFilter{})
	op.onExecute = func(ctx context.Context, s store.OperatorStore) {
		if _, err := s.AddActionLog(ctx, store.NewActionLog{StepID: "step-1", State: store.ActionLogStateSuccess}); err != nil {
			t.Errorf("AddActionLog: %v", err)
		}
	}

	cancel, _ := startRunner(t, operatorrunner.New(st, op).Run)
	defer cancel()

	st.insightCh <- store.Insight{Pattern: "p", Severity: store.SeverityInfo}
	awaitExecute(t, op.called)

	out := sb.String()
	for _, want := range []string{`"msg":"ActionLog written"`, `"component":"operator"`, `"name":"` + name + `"`, `"step_id":"step-1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %s\ngot: %s", want, out)
		}
	}
}

// TestRun_RecordedAction_LogsAtInfo verifies the operator runner emits the
// terminal recorded-action event at Info, scoped with component and name.
func TestRun_RecordedAction_LogsAtInfo(t *testing.T) {
	const name = "my-operator"
	var sb safeBuf
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&sb, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })

	st := newSpyStore()
	op := newSpyOp(name, store.InsightFilter{})
	cancel, done := startRunner(t, operatorrunner.New(st, op).Run)

	st.insightCh <- store.Insight{ID: "i1", Pattern: "p", Severity: store.SeverityInfo}
	awaitExecute(t, op.called)
	cancel()
	if err := awaitDone(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runner exited with error: %v", err)
	}

	out := sb.String()
	for _, want := range []string{`"msg":"RecordedAction written"`, `"component":"operator"`, `"name":"` + name + `"`, `"state":"success"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %s\ngot: %s", want, out)
		}
	}
}
