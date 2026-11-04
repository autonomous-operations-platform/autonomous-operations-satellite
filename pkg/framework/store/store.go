package store

import (
	"context"
	"errors"
)

var (
	ErrInvalidID    = errors.New("store: invalid record ID (expected UUIDv7)")
	ErrInvalidQuery = errors.New("store: invalid query parameters")
	ErrStoreClosed  = errors.New("store: store is closed")
)

// CollectorStore is the store surface exposed to Collectors.
type CollectorStore interface {
	AddTelemetry(ctx context.Context, records []NewTelemetryRecord) ([]RecordID, error)
}

// ObserverStore is the store surface exposed to Observers.
type ObserverStore interface {
	AddInsight(ctx context.Context, insight NewInsight) (RecordID, error)
	QueryTelemetryByTime(ctx context.Context, q TelemetryTimeRangeQuery) (QueryResult[TelemetryRecord], error)
	QueryTelemetryFromCursor(ctx context.Context, q TelemetryCursorQuery) (QueryResult[TelemetryRecord], error)
}

// OperatorStore is the store surface exposed to Operators.
type OperatorStore interface {
	AddActionLog(ctx context.Context, log NewActionLog) (RecordID, error)
	QueryTelemetryByTime(ctx context.Context, q TelemetryTimeRangeQuery) (QueryResult[TelemetryRecord], error)
	QueryTelemetryFromCursor(ctx context.Context, q TelemetryCursorQuery) (QueryResult[TelemetryRecord], error)
}

// Store is the full interface held by runners. Components receive only their
// scoped subset defined above.
type Store interface {
	AddTelemetry(ctx context.Context, records []TelemetryRecord) error
	SubscribeTelemetry(ctx context.Context, filter TelemetryFilter) (<-chan TelemetryRecord, error)

	AddInsight(ctx context.Context, insight Insight) error
	SubscribeInsights(ctx context.Context, filter InsightFilter) (<-chan Insight, error)

	AddActionLog(ctx context.Context, log ActionLog) error
	AddRecordedAction(ctx context.Context, action RecordedAction) error

	QueryTelemetryByTime(ctx context.Context, q TelemetryTimeRangeQuery) (QueryResult[TelemetryRecord], error)
	QueryTelemetryFromCursor(ctx context.Context, q TelemetryCursorQuery) (QueryResult[TelemetryRecord], error)

	Close()
}
