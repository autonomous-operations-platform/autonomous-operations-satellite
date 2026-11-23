package store

import (
	"fmt"
	"time"
)

// TelemetryTimeField selects which timestamp column a time-range query filters
// and orders by. The zero value filters by ReceivedAt, keeping existing callers
// unchanged.
type TelemetryTimeField string

const (
	// ByReceivedAt filters by when the framework ingested the record. Default.
	ByReceivedAt TelemetryTimeField = ""
	// BySourceTime filters by when the source observed the event.
	BySourceTime TelemetryTimeField = "source_time"
)

type TelemetryTimeRangeQuery struct {
	Start      time.Time
	End        time.Time
	Attributes map[string]string
	// TimeField selects the timestamp column to filter and order by.
	// Zero value (ByReceivedAt) preserves the original ReceivedAt behavior.
	TimeField TelemetryTimeField
}

func (q TelemetryTimeRangeQuery) Validate() error {
	if q.Start.IsZero() && q.End.IsZero() {
		return fmt.Errorf("TelemetryTimeRangeQuery: both Start and End are zero")
	}
	if !q.End.IsZero() && q.Start.After(q.End) {
		return fmt.Errorf("TelemetryTimeRangeQuery: Start (%v) is after End (%v)", q.Start, q.End)
	}
	switch q.TimeField {
	case ByReceivedAt, BySourceTime:
	default:
		return fmt.Errorf("TelemetryTimeRangeQuery: unknown TimeField %q", q.TimeField)
	}
	return nil
}

type TelemetryCursorQuery struct {
	AfterID    RecordID
	Count      int
	Attributes map[string]string
}

func (q TelemetryCursorQuery) Validate() error {
	if q.Count <= 0 {
		return fmt.Errorf("TelemetryCursorQuery: Count must be > 0, got %d", q.Count)
	}
	return nil
}

type QueryResult[T any] struct {
	Records    []T      `json:"records"`
	NextCursor RecordID `json:"next_cursor,omitempty"`
}
