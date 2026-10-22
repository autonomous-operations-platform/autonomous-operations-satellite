package collectorstorewrapper_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/internal/collectorstorewrapper"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/store/memstore"
)

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNew_PanicsOnNilStore(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for nil store")
		}
	}()
	collectorstorewrapper.New(nil, nil)
}

// ---------------------------------------------------------------------------
// Spy store — captures AddTelemetry calls.
// ---------------------------------------------------------------------------

type spyStore struct {
	received []store.TelemetryRecord
	retErr   error
}

func (s *spyStore) AddTelemetry(_ context.Context, records []store.TelemetryRecord) error {
	s.received = append(s.received, records...)
	return s.retErr
}
func (s *spyStore) SubscribeTelemetry(context.Context, store.TelemetryFilter) (<-chan store.TelemetryRecord, error) {
	return nil, nil
}
func (s *spyStore) AddInsight(context.Context, store.Insight) error { return nil }
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
// AddTelemetry
// ---------------------------------------------------------------------------

func TestAddTelemetry_GeneratesUniqueIDs(t *testing.T) {
	spy := &spyStore{}
	w := collectorstorewrapper.New(spy, nil)

	ids, err := w.AddTelemetry(context.Background(), []store.NewTelemetryRecord{
		{Signal: store.SignalMetrics},
		{Signal: store.SignalLogs},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("want 2 IDs, got %d", len(ids))
	}
	if ids[0] == ids[1] {
		t.Error("IDs must be unique")
	}
	if ids[0] == "" || ids[1] == "" {
		t.Error("IDs must not be empty")
	}
}

func TestAddTelemetry_ReceivedAtFromID(t *testing.T) {
	// This test ensures that the timestamp in ReceivedAt and the one embedded
	// in the record ID are exactly the same and not a few microseconds off.
	const fixedID = store.RecordID("018cc251-f400-7abc-8def-123456789abc")
	fixedTime := time.UnixMilli(1704067200000).UTC() // 2024-01-01T00:00:00.000Z, as encoded in the fixed uuidv7 id

	spy := &spyStore{}
	w := collectorstorewrapper.NewWithIDGenerator(spy, func() store.RecordID { return fixedID })

	if _, err := w.AddTelemetry(context.Background(), []store.NewTelemetryRecord{
		{Signal: store.SignalMetrics},
	}); err != nil {
		t.Fatal(err)
	}
	if len(spy.received) != 1 {
		t.Fatalf("want 1 stored record, got %d", len(spy.received))
	}
	if !spy.received[0].ReceivedAt.Equal(fixedTime) {
		t.Errorf("ReceivedAt %v does not match fixedTime %v", spy.received[0].ReceivedAt, fixedTime)
	}
}

func TestAddTelemetry_IDsMatchRecords(t *testing.T) {
	spy := &spyStore{}
	w := collectorstorewrapper.New(spy, nil)

	ids, err := w.AddTelemetry(context.Background(), []store.NewTelemetryRecord{
		{Signal: store.SignalMetrics},
		{Signal: store.SignalLogs},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if spy.received[i].ID != id {
			t.Errorf("record[%d].ID = %s, want %s", i, spy.received[i].ID, id)
		}
	}
}

func TestAddTelemetry_FieldsPassedThrough(t *testing.T) {
	spy := &spyStore{}
	w := collectorstorewrapper.New(spy, nil)

	payload := []byte("test-payload")
	attrs := map[string]string{"host": "node-1"}
	_, err := w.AddTelemetry(context.Background(), []store.NewTelemetryRecord{
		{Signal: store.SignalMetrics, Payload: payload, Attributes: attrs},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := spy.received[0]
	if rec.Signal != store.SignalMetrics {
		t.Errorf("Signal: got %s, want metrics", rec.Signal)
	}
	if string(rec.Payload) != string(payload) {
		t.Errorf("Payload: got %q, want %q", rec.Payload, payload)
	}
	if rec.Attributes["host"] != "node-1" {
		t.Errorf("Attributes: want host=node-1, got %v", rec.Attributes)
	}
}

func TestAddTelemetry_PropagatesError(t *testing.T) {
	want := errors.New("store error")
	spy := &spyStore{retErr: want}
	w := collectorstorewrapper.New(spy, nil)

	_, err := w.AddTelemetry(context.Background(), []store.NewTelemetryRecord{{Signal: store.SignalMetrics}})
	if !errors.Is(err, want) {
		t.Errorf("want %v, got %v", want, err)
	}
}

func TestAddTelemetry_EmptySlice(t *testing.T) {
	spy := &spyStore{}
	w := collectorstorewrapper.New(spy, nil)

	ids, err := w.AddTelemetry(context.Background(), []store.NewTelemetryRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("want 0 IDs for empty input, got %d", len(ids))
	}
}

func TestAddTelemetry_LogsAtDebug(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	w := collectorstorewrapper.New(&spyStore{}, slog.New(handler))

	_, err := w.AddTelemetry(context.Background(), []store.NewTelemetryRecord{
		{Signal: store.SignalMetrics},
		{Signal: store.SignalLogs},
	})
	if err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	for _, want := range []string{`"msg":"Telemetry written"`, `"count":2`} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %s\ngot: %s", want, out)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration — round-trip through memstore
// ---------------------------------------------------------------------------

func TestAddTelemetry_RoundTripThroughMemstore(t *testing.T) {
	ms := memstore.New()
	w := collectorstorewrapper.New(ms, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := ms.SubscribeTelemetry(ctx, store.TelemetryFilter{})
	if err != nil {
		t.Fatal(err)
	}

	ids, err := w.AddTelemetry(ctx, []store.NewTelemetryRecord{
		{Signal: store.SignalMetrics, Payload: []byte("data")},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case rec := <-ch:
		if rec.ID != ids[0] {
			t.Errorf("ID: got %s, want %s", rec.ID, ids[0])
		}
		if !rec.ReceivedAt.Equal(ids[0].Time()) {
			t.Errorf("ReceivedAt %v does not match id.Time() %v", rec.ReceivedAt, ids[0].Time())
		}
	default:
		t.Fatal("no record received from memstore")
	}
}
