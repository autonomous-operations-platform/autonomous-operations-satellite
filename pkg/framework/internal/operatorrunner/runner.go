package operatorrunner

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/operator"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

type Runner struct {
	operator operator.Operator
	store    store.Store
	logger   *slog.Logger
	ready    chan struct{}
	idGen    func() store.RecordID
}

func (r *Runner) newID() store.RecordID {
	if r.idGen != nil {
		return r.idGen()
	}
	return store.NewID()
}

func New(s store.Store, op operator.Operator) *Runner {
	if s == nil {
		panic("operatorrunner: store must not be nil")
	}
	if op == nil {
		panic("operatorrunner: operator must not be nil")
	}
	return &Runner{
		store:    s,
		operator: op,
		logger:   slog.Default().With("component", "operator", "name", op.Name()),
		ready:    make(chan struct{}),
	}
}

// Ready returns a channel that is closed once the runner has completed its
// subscribe call, successfully or not.
func (r *Runner) Ready() <-chan struct{} { return r.ready }

func (r *Runner) Run(ctx context.Context) error {
	f := r.operator.Filter()
	ch, err := r.store.SubscribeInsights(ctx, f)
	close(r.ready)
	if err != nil {
		return fmt.Errorf("operator %q: subscribe: %w", r.operator.Name(), err)
	}
	r.logger.InfoContext(ctx, "subscribed — waiting for insights", "patterns", f.Patterns, "severities", f.Severities)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case insight, ok := <-ch:
			if !ok {
				return nil
			}
			r.handleInsight(ctx, insight)
		}
	}
}

func (r *Runner) handleInsight(ctx context.Context, insight store.Insight) {
	executionID := r.newID()

	startedID := r.newID()
	if werr := r.store.AddRecordedAction(ctx, store.RecordedAction{
		ID:           startedID,
		ExecutionID:  executionID,
		InsightID:    insight.ID,
		OperatorName: r.operator.Name(),
		State:        store.StateStarted,
		Timestamp:    startedID.Time(),
	}); werr != nil {
		r.logger.ErrorContext(ctx, "failed to write started RecordedAction", "err", werr)
	}

	defer func() {
		if rec := recover(); rec != nil {
			msg := fmt.Sprintf("panic: %v", rec)
			r.logger.ErrorContext(ctx, "operator panicked", "err", msg)
			panicID := r.newID()
			_ = r.store.AddRecordedAction(ctx, store.RecordedAction{
				ID:           panicID,
				ExecutionID:  executionID,
				InsightID:    insight.ID,
				OperatorName: r.operator.Name(),
				State:        store.StateFailed,
				Timestamp:    panicID.Time(),
			})
		}
	}()

	execLogger := r.logger.With("execution_id", executionID, "insight_id", insight.ID)

	execLogger.InfoContext(ctx, "processing Insight",
		"pattern", insight.Pattern,
		"severity", insight.Severity,
	)

	wrapped := &storeWrapper{inner: r.store, executionID: executionID, logger: execLogger}
	err := r.operator.Execute(ctx, execLogger, insight, wrapped)

	state := store.StateSuccess
	if err != nil {
		state = store.StateFailed
	}

	id := r.newID()
	werr := r.store.AddRecordedAction(ctx, store.RecordedAction{
		ID:           id,
		ExecutionID:  executionID,
		InsightID:    insight.ID,
		OperatorName: r.operator.Name(),
		State:        state,
		Timestamp:    id.Time(),
	})
	if werr != nil {
		execLogger.ErrorContext(ctx, "failed to write final RecordedAction", "err", werr)
	}

	if err != nil {
		execLogger.ErrorContext(ctx, "execution failed", "err", err, "state", state)
		return
	}
	execLogger.InfoContext(ctx, "RecordedAction written", "recorded_action_id", id, "state", state)
}

type storeWrapper struct {
	inner       store.Store
	executionID store.RecordID
	logger      *slog.Logger
	idGen       func() store.RecordID
}

func (w *storeWrapper) newID() store.RecordID {
	if w.idGen != nil {
		return w.idGen()
	}
	return store.NewID()
}

func (w *storeWrapper) AddActionLog(ctx context.Context, log store.NewActionLog) (store.RecordID, error) {
	id := w.newID()
	err := w.inner.AddActionLog(ctx, store.ActionLog{
		ID:          id,
		ExecutionID: w.executionID,
		StepID:      log.StepID,
		Description: log.Description,
		State:       log.State,
		Timestamp:   id.Time(),
	})
	if err == nil {
		w.logger.InfoContext(ctx, "ActionLog written",
			"action_log_id", id, "execution_id", w.executionID,
			"step_id", log.StepID, "state", log.State, "description", log.Description)
	}
	return id, err
}

func (w *storeWrapper) QueryTelemetryByTime(ctx context.Context, q store.TelemetryTimeRangeQuery) (store.QueryResult[store.TelemetryRecord], error) {
	return w.inner.QueryTelemetryByTime(ctx, q)
}

func (w *storeWrapper) QueryTelemetryFromCursor(ctx context.Context, q store.TelemetryCursorQuery) (store.QueryResult[store.TelemetryRecord], error) {
	return w.inner.QueryTelemetryFromCursor(ctx, q)
}
