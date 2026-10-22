package observerrunner

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/observer"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

type Runner struct {
	observer observer.Observer
	store    store.Store
	logger   *slog.Logger
	ready    chan struct{}
}

func New(s store.Store, obs observer.Observer) *Runner {
	if s == nil {
		panic("observerrunner: store must not be nil")
	}
	if obs == nil {
		panic("observerrunner: observer must not be nil")
	}
	return &Runner{
		store:    s,
		observer: obs,
		logger:   slog.Default().With("component", "observer", "name", obs.Name()),
		ready:    make(chan struct{}),
	}
}

// Ready returns a channel that is closed once the runner has completed its
// subscribe call, successfully or not.
func (r *Runner) Ready() <-chan struct{} { return r.ready }

func (r *Runner) Run(ctx context.Context) error {
	f := r.observer.Filter()
	ch, err := r.store.SubscribeTelemetry(ctx, f)
	close(r.ready)
	if err != nil {
		return fmt.Errorf("observer %q: subscribe: %w", r.observer.Name(), err)
	}
	r.logger.Info("subscribed — waiting for telemetry", "signal", f.Signal, "attrs", f.Attributes)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case rec, ok := <-ch:
			if !ok {
				return nil
			}
			wrapped := &storeWrapper{inner: r.store, observerName: r.observer.Name(), logger: r.logger}
			if err := r.observer.Detect(ctx, r.logger, rec, wrapped); err != nil {
				r.logger.Error("detect error", "err", err)
			}
		}
	}
}

type storeWrapper struct {
	inner        store.Store
	observerName string
	logger       *slog.Logger
	idGen        func() store.RecordID
}

func (w *storeWrapper) newID() store.RecordID {
	if w.idGen != nil {
		return w.idGen()
	}
	return store.NewID()
}

func (w *storeWrapper) AddInsight(ctx context.Context, ni store.NewInsight) (store.RecordID, error) {
	id := w.newID()
	err := w.inner.AddInsight(ctx, store.Insight{
		ID:              id,
		ObserverName:    w.observerName,
		Pattern:         ni.Pattern,
		Severity:        ni.Severity,
		Summary:         ni.Summary,
		WindowStart:     ni.WindowStart,
		WindowEnd:       ni.WindowEnd,
		SourceRecordIDs: ni.SourceRecordIDs,
		CreatedAt:       id.Time(),
	})
	if err == nil {
		w.logger.Info("Insight written",
			"insight_id", id, "pattern", ni.Pattern, "severity", ni.Severity, "summary", ni.Summary)
	}
	return id, err
}

func (w *storeWrapper) QueryTelemetryByTime(ctx context.Context, q store.TelemetryTimeRangeQuery) (store.QueryResult[store.TelemetryRecord], error) {
	return w.inner.QueryTelemetryByTime(ctx, q)
}

func (w *storeWrapper) QueryTelemetryFromCursor(ctx context.Context, q store.TelemetryCursorQuery) (store.QueryResult[store.TelemetryRecord], error) {
	return w.inner.QueryTelemetryFromCursor(ctx, q)
}
