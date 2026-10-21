package collectorstorewrapper

import (
	"context"
	"log/slog"

	"github.com/autonomous-operations/aos-framework/pkg/framework/store"
)

// StoreWrapper adapts store.Store to store.CollectorStore.
// It generates a UUIDv7 ID for each record and derives ReceivedAt from that
// ID's embedded timestamp, keeping the two values in sync.
type StoreWrapper struct {
	inner  store.Store
	logger *slog.Logger
	idGen  func() store.RecordID
}

func New(s store.Store, log *slog.Logger) *StoreWrapper {
	if s == nil {
		panic("collectorstorewrapper: store must not be nil")
	}
	if log == nil {
		log = slog.Default()
	}
	return &StoreWrapper{inner: s, logger: log}
}

func (w *StoreWrapper) newID() store.RecordID {
	if w.idGen != nil {
		return w.idGen()
	}
	return store.NewID()
}

func (w *StoreWrapper) AddTelemetry(ctx context.Context, records []store.NewTelemetryRecord) ([]store.RecordID, error) {
	full := make([]store.TelemetryRecord, len(records))
	ids := make([]store.RecordID, len(records))
	for i, r := range records {
		id := w.newID()
		ids[i] = id
		full[i] = store.TelemetryRecord{
			ID:         id,
			Signal:     r.Signal,
			ReceivedAt: id.Time(),
			Payload:    r.Payload,
			Attributes: r.Attributes,
		}
	}
	if err := w.inner.AddTelemetry(ctx, full); err != nil {
		return nil, err
	}
	w.logger.Debug("Telemetry written", "count", len(records))
	return ids, nil
}
