package store

import (
	"fmt"
	"time"
)

type TelemetryTimeRangeQuery struct {
	Start      time.Time
	End        time.Time
	Attributes map[string]string
}

func (q TelemetryTimeRangeQuery) Validate() error {
	if q.Start.IsZero() && q.End.IsZero() {
		return fmt.Errorf("TelemetryTimeRangeQuery: both Start and End are zero")
	}
	if !q.End.IsZero() && q.Start.After(q.End) {
		return fmt.Errorf("TelemetryTimeRangeQuery: Start (%v) is after End (%v)", q.Start, q.End)
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
