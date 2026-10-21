package memstore_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/autonomous-operations/aos-framework/pkg/framework/store"
	"github.com/autonomous-operations/aos-framework/pkg/store/memstore"
)

func recvTelemetry(t *testing.T, ch <-chan store.TelemetryRecord) store.TelemetryRecord {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for TelemetryRecord")
		return store.TelemetryRecord{}
	}
}

func recvInsight(t *testing.T, ch <-chan store.Insight) store.Insight {
	t.Helper()
	select {
	case i := <-ch:
		return i
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Insight")
		return store.Insight{}
	}
}

// ---------------------------------------------------------------------------
// Telemetry
// ---------------------------------------------------------------------------

func TestAddTelemetry_PassesThrough(t *testing.T) {
	s := memstore.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
	if err != nil {
		t.Fatal(err)
	}

	id1, id2 := store.NewID(), store.NewID()
	if err := s.AddTelemetry(context.Background(), []store.TelemetryRecord{
		{ID: id1, Signal: store.SignalMetrics},
		{ID: id2, Signal: store.SignalLogs},
	}); err != nil {
		t.Fatal(err)
	}

	r1 := recvTelemetry(t, ch)
	r2 := recvTelemetry(t, ch)
	if r1.ID != id1 || r2.ID != id2 {
		t.Fatalf("ID mismatch: got (%s, %s), want (%s, %s)", r1.ID, r2.ID, id1, id2)
	}
}

func TestAddTelemetry_FieldsRoundTrip(t *testing.T) {
	s := memstore.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
	if err != nil {
		t.Fatal(err)
	}

	id := store.NewID()
	payload := []byte("hello")
	attrs := map[string]string{"host": "node-1"}
	receivedAt := id.Time()
	if err := s.AddTelemetry(context.Background(), []store.TelemetryRecord{
		{ID: id, Signal: store.SignalMetrics, Payload: payload, Attributes: attrs, ReceivedAt: receivedAt},
	}); err != nil {
		t.Fatal(err)
	}

	r := recvTelemetry(t, ch)
	if r.ID != id {
		t.Errorf("ID: got %s, want %s", r.ID, id)
	}
	if r.Signal != store.SignalMetrics {
		t.Errorf("Signal: got %s, want metrics", r.Signal)
	}
	if string(r.Payload) != string(payload) {
		t.Errorf("Payload: got %q, want %q", r.Payload, payload)
	}
	if r.Attributes["host"] != "node-1" {
		t.Errorf("Attributes: want host=node-1, got %v", r.Attributes)
	}
	if !r.ReceivedAt.Equal(receivedAt) {
		t.Errorf("ReceivedAt: got %v, want %v", r.ReceivedAt, receivedAt)
	}
}

func TestSubscribeTelemetry_FilterBySignal(t *testing.T) {
	s := memstore.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{Signal: store.SignalMetrics})
	if err := s.AddTelemetry(context.Background(), []store.TelemetryRecord{
		{ID: store.NewID(), Signal: store.SignalLogs},    // must not arrive
		{ID: store.NewID(), Signal: store.SignalMetrics}, // must arrive
	}); err != nil {
		t.Fatal(err)
	}

	r := recvTelemetry(t, ch)
	if r.Signal != store.SignalMetrics {
		t.Errorf("got signal %s, want metrics", r.Signal)
	}
	select {
	case extra := <-ch:
		t.Errorf("unexpected extra record: %v", extra)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSubscribeTelemetry_ChannelClosesOnCancel(t *testing.T) {
	s := memstore.New()
	ctx, cancel := context.WithCancel(context.Background())

	ch, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
	cancel()

	select {
	case _, open := <-ch:
		if open {
			t.Error("expected channel to be closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after ctx cancel")
	}
}

func TestSubscribeTelemetry_MultipleSubscribers(t *testing.T) {
	s := memstore.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch1, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
	ch2, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
	if err := s.AddTelemetry(context.Background(), []store.TelemetryRecord{{ID: store.NewID(), Signal: store.SignalMetrics}}); err != nil {
		t.Fatal(err)
	}

	recvTelemetry(t, ch1)
	recvTelemetry(t, ch2)
}

func TestSubscribeTelemetry_DropOldest(t *testing.T) {
	const bufSize = 4
	s := memstore.New(&memstore.Config{BufSize: bufSize})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})

	// Add bufSize+1 records without reading — the oldest must be dropped.
	var ids []store.RecordID
	for i := range bufSize + 1 {
		id := store.NewID()
		ids = append(ids, id)
		if err := s.AddTelemetry(context.Background(), []store.TelemetryRecord{
			{ID: id, Signal: store.SignalMetrics, Payload: []byte{byte(i)}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	var received []store.RecordID
	for range bufSize {
		received = append(received, recvTelemetry(t, ch).ID)
	}

	select {
	case extra := <-ch:
		t.Errorf("expected empty channel after drain, got %s", extra.ID)
	default:
	}

	if received[0] == ids[0] {
		t.Error("oldest record should have been dropped but was present")
	}
	if received[0] != ids[1] {
		t.Errorf("first received: got %s, want %s", received[0], ids[1])
	}
	if received[bufSize-1] != ids[bufSize] {
		t.Errorf("last received: got %s, want %s", received[bufSize-1], ids[bufSize])
	}
}

// ---------------------------------------------------------------------------
// Insights
// ---------------------------------------------------------------------------

func TestAddInsight_FieldsPassThrough(t *testing.T) {
	s := memstore.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := s.SubscribeInsights(ctx, store.InsightFilter{})

	id := store.NewID()
	createdAt := id.Time()
	if err := s.AddInsight(context.Background(), store.Insight{
		ID:        id,
		Pattern:   "high-error-rate",
		Severity:  store.SeverityCritical,
		CreatedAt: createdAt,
	}); err != nil {
		t.Fatalf("AddInsight: err=%v", err)
	}

	got := recvInsight(t, ch)
	if got.ID != id {
		t.Errorf("ID: got %s, want %s", got.ID, id)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, createdAt)
	}
}

func TestAddInsight_PreservesProvidedValues(t *testing.T) {
	s := memstore.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := s.SubscribeInsights(ctx, store.InsightFilter{})

	fixedID := store.NewID()
	fixedTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.AddInsight(context.Background(), store.Insight{
		ID:        fixedID,
		CreatedAt: fixedTime,
		Pattern:   "test",
		Severity:  store.SeverityInfo,
	}); err != nil {
		t.Fatal(err)
	}

	got := recvInsight(t, ch)
	if got.ID != fixedID {
		t.Errorf("ID: got %s, want %s", got.ID, fixedID)
	}
	if !got.CreatedAt.Equal(fixedTime) {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, fixedTime)
	}
}

func TestSubscribeInsights_FilterByPattern(t *testing.T) {
	s := memstore.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := s.SubscribeInsights(ctx, store.InsightFilter{Patterns: []string{"target"}})
	if err := s.AddInsight(context.Background(), store.Insight{ID: store.NewID(), Pattern: "other", Severity: store.SeverityInfo}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddInsight(context.Background(), store.Insight{ID: store.NewID(), Pattern: "target", Severity: store.SeverityInfo}); err != nil {
		t.Fatal(err)
	}

	got := recvInsight(t, ch)
	if got.Pattern != "target" {
		t.Errorf("got pattern %q, want target", got.Pattern)
	}
	select {
	case extra := <-ch:
		t.Errorf("unexpected extra insight: %v", extra)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSubscribeInsights_ChannelClosesOnCancel(t *testing.T) {
	s := memstore.New()
	ctx, cancel := context.WithCancel(context.Background())

	ch, _ := s.SubscribeInsights(ctx, store.InsightFilter{})
	cancel()

	select {
	case _, open := <-ch:
		if open {
			t.Error("expected channel to be closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after ctx cancel")
	}
}

// ---------------------------------------------------------------------------
// Action logs and recorded actions
// ---------------------------------------------------------------------------

func TestAddActionLog_StoresPassed(t *testing.T) {
	s := memstore.New()
	id := store.NewID()
	if err := s.AddActionLog(context.Background(), store.ActionLog{
		ID:          id,
		Description: "pod restarted",
		State:       store.ActionLogStateSuccess,
	}); err != nil {
		t.Fatalf("AddActionLog: %v", err)
	}
}

func TestAddRecordedAction_StoresPassed(t *testing.T) {
	s := memstore.New()
	fixedID := store.NewID()
	if err := s.AddRecordedAction(context.Background(), store.RecordedAction{
		ID:    fixedID,
		State: store.StateSuccess,
	}); err != nil {
		t.Fatalf("AddRecordedAction: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Closed store
// ---------------------------------------------------------------------------

func TestClosedStore_RejectsAll(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		fn   func(*memstore.Store) error
	}{
		{"AddTelemetry", func(s *memstore.Store) error {
			return s.AddTelemetry(ctx, []store.TelemetryRecord{{ID: store.NewID(), Signal: store.SignalMetrics}})
		}},
		{"SubscribeTelemetry", func(s *memstore.Store) error {
			_, err := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
			return err
		}},
		{"AddInsight", func(s *memstore.Store) error {
			return s.AddInsight(ctx, store.Insight{ID: store.NewID()})
		}},
		{"SubscribeInsights", func(s *memstore.Store) error {
			_, err := s.SubscribeInsights(ctx, store.InsightFilter{})
			return err
		}},
		{"AddActionLog", func(s *memstore.Store) error {
			return s.AddActionLog(ctx, store.ActionLog{ID: store.NewID()})
		}},
		{"AddRecordedAction", func(s *memstore.Store) error {
			return s.AddRecordedAction(ctx, store.RecordedAction{ID: store.NewID()})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := memstore.New()
			s.Close()
			if err := tc.fn(s); !errors.Is(err, store.ErrStoreClosed) {
				t.Errorf("want ErrStoreClosed, got %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Queries
// ---------------------------------------------------------------------------

func TestQueryTelemetryByTime_TimeRange(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.AddTelemetry(ctx, []store.TelemetryRecord{{ID: store.NewID(), Signal: store.SignalMetrics, ReceivedAt: now}}); err != nil {
		t.Fatal(err)
	}

	// Wide window — must include the record.
	res, err := s.QueryTelemetryByTime(ctx, store.TelemetryTimeRangeQuery{
		Start: time.Now().UTC().Add(-time.Minute),
		End:   time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 {
		t.Errorf("wide range: want 1 record, got %d", len(res.Records))
	}

	// Future Start — no records.
	res, _ = s.QueryTelemetryByTime(ctx, store.TelemetryTimeRangeQuery{
		Start: time.Now().UTC().Add(time.Hour),
	})
	if len(res.Records) != 0 {
		t.Errorf("future Start: want 0 records, got %d", len(res.Records))
	}

	// Past End — no records.
	res, _ = s.QueryTelemetryByTime(ctx, store.TelemetryTimeRangeQuery{
		End: time.Now().UTC().Add(-time.Hour),
	})
	if len(res.Records) != 0 {
		t.Errorf("past End: want 0 records, got %d", len(res.Records))
	}
}

func TestQueryTelemetryByTime_AttributeFilter(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()

	if err := s.AddTelemetry(ctx, []store.TelemetryRecord{
		{ID: store.NewID(), Signal: store.SignalMetrics, ReceivedAt: time.Now().UTC(), Attributes: map[string]string{"host": "node-1"}},
		{ID: store.NewID(), Signal: store.SignalMetrics, ReceivedAt: time.Now().UTC(), Attributes: map[string]string{"host": "node-2"}},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := s.QueryTelemetryByTime(ctx, store.TelemetryTimeRangeQuery{
		Start:      time.Now().UTC().Add(-time.Minute),
		Attributes: map[string]string{"host": "node-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 || res.Records[0].Attributes["host"] != "node-1" {
		t.Errorf("attribute filter: want 1 node-1 record, got %v", res.Records)
	}
}

func TestQueryTelemetryByTime_InvalidQuery(t *testing.T) {
	s := memstore.New()
	_, err := s.QueryTelemetryByTime(context.Background(), store.TelemetryTimeRangeQuery{})
	if err == nil {
		t.Fatal("want error for zero Start and End")
	}
}

func TestQueryTelemetryFromCursor_Pagination(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()

	var allIDs []store.RecordID
	for range 5 {
		id := store.NewID()
		allIDs = append(allIDs, id)
		if err := s.AddTelemetry(ctx, []store.TelemetryRecord{{ID: id, Signal: store.SignalMetrics}}); err != nil {
			t.Fatal(err)
		}
	}

	// Page 1: from beginning, limit 2.
	res, err := s.QueryTelemetryFromCursor(ctx, store.TelemetryCursorQuery{Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 2 || res.Records[0].ID != allIDs[0] || res.Records[1].ID != allIDs[1] {
		t.Fatalf("page 1: unexpected records %v", res.Records)
	}
	if res.NextCursor != allIDs[1] {
		t.Errorf("page 1 NextCursor: got %s, want %s", res.NextCursor, allIDs[1])
	}

	// Page 2: after allIDs[1], limit 2.
	res, _ = s.QueryTelemetryFromCursor(ctx, store.TelemetryCursorQuery{AfterID: allIDs[1], Count: 2})
	if len(res.Records) != 2 || res.Records[0].ID != allIDs[2] || res.Records[1].ID != allIDs[3] {
		t.Fatalf("page 2: unexpected records %v", res.Records)
	}

	// Page 3: after allIDs[3], only 1 record remains — NextCursor must be empty.
	res, _ = s.QueryTelemetryFromCursor(ctx, store.TelemetryCursorQuery{AfterID: allIDs[3], Count: 2})
	if len(res.Records) != 1 || res.Records[0].ID != allIDs[4] {
		t.Fatalf("page 3: want [allIDs[4]], got %v", res.Records)
	}
	if res.NextCursor != "" {
		t.Errorf("page 3 NextCursor: want empty at end, got %s", res.NextCursor)
	}
}

func TestQueryTelemetryFromCursor_InvalidQuery(t *testing.T) {
	s := memstore.New()
	_, err := s.QueryTelemetryFromCursor(context.Background(), store.TelemetryCursorQuery{Count: 0})
	if err == nil {
		t.Fatal("want error for Count=0")
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestStore_Concurrent(t *testing.T) {
	s := memstore.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
	go func() {
		for range ch {
		}
	}()

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if err := s.AddTelemetry(context.Background(), []store.TelemetryRecord{
					{ID: store.NewID(), Signal: store.SignalMetrics},
				}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}
