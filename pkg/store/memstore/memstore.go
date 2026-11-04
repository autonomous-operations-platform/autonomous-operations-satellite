package memstore

import (
	"context"
	"sync"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

// Config holds optional Store configuration. A nil Config uses defaults.
type Config struct {
	BufSize int // subscriber channel capacity; defaults to 256
}

// sendOrDrop delivers v to ch. When ch is full it drops the oldest entry first,
// preserving the drop-oldest back-pressure behaviour described in the design doc.
func sendOrDrop[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- v:
		default:
		}
	}
}

type telemetrySub struct {
	filter store.TelemetryFilter
	ch     chan store.TelemetryRecord
}

type insightSub struct {
	filter store.InsightFilter
	ch     chan store.Insight
}

type Store struct {
	mu sync.Mutex

	bufSize int

	telemetry       []store.TelemetryRecord
	insights        []store.Insight
	actionLogs      []store.ActionLog
	recordedActions []store.RecordedAction

	telemetrySubs []*telemetrySub
	insightSubs   []*insightSub

	closed bool
}

// var _ ensures Store satisfies the framework store contract.
var _ store.Store = (*Store)(nil)

func New(cfg ...*Config) *Store {
	bufSize := 256
	if len(cfg) > 0 && cfg[0] != nil && cfg[0].BufSize > 0 {
		bufSize = cfg[0].BufSize
	}
	return &Store{bufSize: bufSize}
}

// ---------------------------------------------------------------------------
// Telemetry
// ---------------------------------------------------------------------------

func (s *Store) AddTelemetry(ctx context.Context, records []store.TelemetryRecord) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return store.ErrStoreClosed
	}
	s.telemetry = append(s.telemetry, records...)
	subs := s.telemetrySubs[:]
	s.mu.Unlock()

	for _, sub := range subs {
		for _, rec := range records {
			if sub.filter.Matches(rec) {
				sendOrDrop(sub.ch, rec)
			}
		}
	}
	return nil
}

func (s *Store) SubscribeTelemetry(ctx context.Context, filter store.TelemetryFilter) (<-chan store.TelemetryRecord, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, store.ErrStoreClosed
	}
	ch := make(chan store.TelemetryRecord, s.bufSize)
	sub := &telemetrySub{filter: filter, ch: ch}
	s.telemetrySubs = append(s.telemetrySubs, sub)
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		for i, ts := range s.telemetrySubs {
			if ts == sub {
				s.telemetrySubs = append(s.telemetrySubs[:i], s.telemetrySubs[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		close(ch)
	}()
	return ch, nil
}

// ---------------------------------------------------------------------------
// Insights
// ---------------------------------------------------------------------------

func (s *Store) AddInsight(ctx context.Context, insight store.Insight) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return store.ErrStoreClosed
	}
	s.insights = append(s.insights, insight)
	subs := s.insightSubs[:]
	s.mu.Unlock()

	for _, sub := range subs {
		if sub.filter.Matches(insight) {
			sendOrDrop(sub.ch, insight)
		}
	}
	return nil
}

func (s *Store) SubscribeInsights(ctx context.Context, filter store.InsightFilter) (<-chan store.Insight, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, store.ErrStoreClosed
	}
	ch := make(chan store.Insight, s.bufSize)
	sub := &insightSub{filter: filter, ch: ch}
	s.insightSubs = append(s.insightSubs, sub)
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		for i, is := range s.insightSubs {
			if is == sub {
				s.insightSubs = append(s.insightSubs[:i], s.insightSubs[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		close(ch)
	}()
	return ch, nil
}

// ---------------------------------------------------------------------------
// Action logs
// ---------------------------------------------------------------------------

func (s *Store) AddActionLog(ctx context.Context, log store.ActionLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return store.ErrStoreClosed
	}
	s.actionLogs = append(s.actionLogs, log)
	return nil
}

// ---------------------------------------------------------------------------
// Recorded actions
// ---------------------------------------------------------------------------

func (s *Store) AddRecordedAction(ctx context.Context, action store.RecordedAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return store.ErrStoreClosed
	}
	s.recordedActions = append(s.recordedActions, action)
	return nil
}

// ---------------------------------------------------------------------------
// Queries
// ---------------------------------------------------------------------------

func (s *Store) QueryTelemetryByTime(ctx context.Context, q store.TelemetryTimeRangeQuery) (store.QueryResult[store.TelemetryRecord], error) {
	if err := q.Validate(); err != nil {
		return store.QueryResult[store.TelemetryRecord]{}, err
	}
	s.mu.Lock()
	snap := s.telemetry[:]
	s.mu.Unlock()

	var out []store.TelemetryRecord
	for _, r := range snap {
		if !q.Start.IsZero() && r.ReceivedAt.Before(q.Start) {
			continue
		}
		if !q.End.IsZero() && r.ReceivedAt.After(q.End) {
			continue
		}
		if !matchAttrs(r.Attributes, q.Attributes) {
			continue
		}
		out = append(out, r)
	}
	return store.QueryResult[store.TelemetryRecord]{Records: out}, nil
}

func (s *Store) QueryTelemetryFromCursor(ctx context.Context, q store.TelemetryCursorQuery) (store.QueryResult[store.TelemetryRecord], error) {
	if err := q.Validate(); err != nil {
		return store.QueryResult[store.TelemetryRecord]{}, err
	}
	s.mu.Lock()
	snap := s.telemetry[:]
	s.mu.Unlock()

	var out []store.TelemetryRecord
	past := q.AfterID == ""
	for _, r := range snap {
		if !past {
			if r.ID == q.AfterID {
				past = true
			}
			continue
		}
		if !matchAttrs(r.Attributes, q.Attributes) {
			continue
		}
		out = append(out, r)
		if len(out) == q.Count {
			break
		}
	}

	var next store.RecordID
	if len(out) == q.Count {
		next = out[len(out)-1].ID
	}
	return store.QueryResult[store.TelemetryRecord]{Records: out, NextCursor: next}, nil
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func matchAttrs(record, query map[string]string) bool {
	for k, v := range query {
		if record[k] != v {
			return false
		}
	}
	return true
}
