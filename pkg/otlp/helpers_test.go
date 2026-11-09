package otlp_test

import (
	"context"
	"sync"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

// fakeStore records every AddTelemetry call and can fail on demand.
type fakeStore struct {
	mu      sync.Mutex
	calls   int
	records []store.NewTelemetryRecord
	err     error
}

func (s *fakeStore) AddTelemetry(_ context.Context, records []store.NewTelemetryRecord) ([]store.RecordID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	s.records = append(s.records, records...)
	return make([]store.RecordID, len(records)), nil
}

func (s *fakeStore) snapshot() (int, []store.NewTelemetryRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, append([]store.NewTelemetryRecord(nil), s.records...)
}

const validLogs = `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"body":{"stringValue":"hello"}}]}]}]}`

const validMetrics = `{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc"}}]},
"scopeMetrics":[{"metrics":[{"gauge":{"dataPoints":[{"attributes":[{"key":"cpu","value":{"intValue":"0"}}]}]}}]}]}]}`

const validTraces = `{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"op"}]}]}]}`
