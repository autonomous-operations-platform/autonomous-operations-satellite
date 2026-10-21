package doris

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.tools.sap/autonomous-operations/aos-framework/pkg/framework/store"
)

func init() {
	sql.Register("discard", &discardDriver{})
}

type (
	discardDriver struct{}
	discardConn   struct{}
	discardStmt   struct{}
	discardResult struct{}
	discardRows   struct{}
)

func (d *discardDriver) Open(_ string) (driver.Conn, error)         { return &discardConn{}, nil }
func (c *discardConn) Prepare(_ string) (driver.Stmt, error)        { return &discardStmt{}, nil }
func (c *discardConn) Close() error                                 { return nil }
func (c *discardConn) Begin() (driver.Tx, error)                    { return nil, driver.ErrBadConn }
func (s *discardStmt) Close() error                                 { return nil }
func (s *discardStmt) NumInput() int                                { return -1 }
func (s *discardStmt) Exec(_ []driver.Value) (driver.Result, error) { return &discardResult{}, nil }
func (s *discardStmt) Query(_ []driver.Value) (driver.Rows, error)  { return &discardRows{}, nil }
func (r *discardResult) LastInsertId() (int64, error)               { return 0, nil }
func (r *discardResult) RowsAffected() (int64, error)               { return 0, nil }
func (r *discardRows) Columns() []string                            { return nil }
func (r *discardRows) Close() error                                 { return nil }
func (r *discardRows) Next(_ []driver.Value) error                  { return io.EOF }

func newTestStore(t *testing.T) *DorisStore {
	t.Helper()
	db, err := sql.Open("discard", "")
	if err != nil {
		t.Fatalf("open discard db: %v", err)
	}
	s, err := NewStore(db, Params{}, slog.Default())
	if err != nil {
		_ = db.Close()
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func makeEnv(attrs map[string]string) store.TelemetryRecord {
	id := store.NewID()
	return store.TelemetryRecord{
		ID:         id,
		Signal:     store.SignalMetrics,
		Payload:    []byte(`{}`),
		Attributes: attrs,
		ReceivedAt: id.Time(),
	}
}

func recv[T any](t *testing.T, ch <-chan T, timeout time.Duration) (T, bool) {
	t.Helper()
	select {
	case v, ok := <-ch:
		return v, ok
	case <-time.After(timeout):
		var zero T
		return zero, false
	}
}

func expectNone[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected value received: %v", v)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestSubscribeTelemetry_receivesRecord(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
	if err != nil {
		t.Fatal(err)
	}

	env := makeEnv(nil)
	if err := s.AddTelemetry(context.Background(), []store.TelemetryRecord{env}); err != nil {
		t.Fatal(err)
	}

	got, ok := recv(t, ch, time.Second)
	if !ok {
		t.Fatal("timeout: no record received")
	}
	if got.ID != env.ID {
		t.Fatalf("id mismatch: want %s, got %s", env.ID, got.ID)
	}
}

func TestSubscribeTelemetry_filterMatch(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{
		Attributes: map[string]string{"service.name": "nginx"},
	})

	other := makeEnv(map[string]string{"service.name": "other"})
	nginx := makeEnv(map[string]string{"service.name": "nginx"})
	if err := s.AddTelemetry(context.Background(), []store.TelemetryRecord{other, nginx}); err != nil {
		t.Fatal(err)
	}

	got, ok := recv(t, ch, time.Second)
	if !ok {
		t.Fatal("timeout: expected nginx record")
	}
	if got.Attributes["service.name"] != "nginx" {
		t.Fatalf("want nginx, got %s", got.Attributes["service.name"])
	}
	expectNone(t, ch)
}

func TestSubscribeTelemetry_filterNoMatch(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{
		Attributes: map[string]string{"service.name": "nginx"},
	})

	if err := s.AddTelemetry(context.Background(), []store.TelemetryRecord{
		makeEnv(map[string]string{"service.name": "other"}),
	}); err != nil {
		t.Fatal(err)
	}
	expectNone(t, ch)
}

func TestSubscribeTelemetry_multipleSubscribers(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch1, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
	ch2, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})

	if err := s.AddTelemetry(context.Background(), []store.TelemetryRecord{makeEnv(nil)}); err != nil {
		t.Fatal(err)
	}

	if _, ok := recv(t, ch1, time.Second); !ok {
		t.Error("subscriber 1: timeout")
	}
	if _, ok := recv(t, ch2, time.Second); !ok {
		t.Error("subscriber 2: timeout")
	}
}

func TestSubscribeTelemetry_backpressureDrop(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})

	// send subscriberBufSize+20 records without draining
	envs := make([]store.TelemetryRecord, subscriberBufSize+20)
	for i := range envs {
		envs[i] = makeEnv(nil)
	}
	s.AddTelemetry(context.Background(), envs)

	// drain what made it into the buffer
	received := 0
	for {
		select {
		case <-ch:
			received++
		case <-time.After(30 * time.Millisecond):
			goto done
		}
	}
done:
	if received > subscriberBufSize {
		t.Fatalf("received %d records, channel capacity is %d", received, subscriberBufSize)
	}
}

func TestClose_closesSubscriberChannels(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ch, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
	s.Close()

	_, ok := recv(t, ch, time.Second)
	if ok {
		t.Fatal("channel should be closed after Close()")
	}
}

func TestClose_closesAllSubscriberTypes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	eCh, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
	iCh, _ := s.SubscribeInsights(ctx, store.InsightFilter{})

	s.Close()

	if _, ok := recv(t, eCh, time.Second); ok {
		t.Error("telemetry channel not closed")
	}
	if _, ok := recv(t, iCh, time.Second); ok {
		t.Error("insight channel not closed")
	}
}

var addOps = []struct {
	name string
	add  func(*DorisStore) error
}{
	{"telemetry", func(s *DorisStore) error {
		return s.AddTelemetry(context.Background(), []store.TelemetryRecord{makeEnv(nil)})
	}},
	{"insight", func(s *DorisStore) error {
		return s.AddInsight(context.Background(), store.Insight{ID: store.NewID()})
	}},
	{"recorded action", func(s *DorisStore) error {
		return s.AddRecordedAction(context.Background(), store.RecordedAction{ID: store.NewID()})
	}},
}

var subscribeOps = []struct {
	name      string
	subErr    func(*DorisStore, context.Context) error
	subscribe func(*DorisStore, context.Context) func(*testing.T)
}{
	{
		name: "telemetry",
		subErr: func(s *DorisStore, ctx context.Context) error {
			_, err := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
			return err
		},
		subscribe: func(s *DorisStore, ctx context.Context) func(*testing.T) {
			ch, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
			return func(t *testing.T) {
				if _, ok := recv(t, ch, time.Second); ok {
					t.Fatal("channel should be closed")
				}
			}
		},
	},
	{
		name: "insights",
		subErr: func(s *DorisStore, ctx context.Context) error {
			_, err := s.SubscribeInsights(ctx, store.InsightFilter{})
			return err
		},
		subscribe: func(s *DorisStore, ctx context.Context) func(*testing.T) {
			ch, _ := s.SubscribeInsights(ctx, store.InsightFilter{})
			return func(t *testing.T) {
				if _, ok := recv(t, ch, time.Second); ok {
					t.Fatal("channel should be closed")
				}
			}
		},
	},
}

func TestAdd_afterCloseReturnsErrStoreClosed(t *testing.T) {
	for _, op := range addOps {
		t.Run(op.name, func(t *testing.T) {
			s := newTestStore(t)
			s.Close()
			if err := op.add(s); !errors.Is(err, store.ErrStoreClosed) {
				t.Fatalf("want ErrStoreClosed, got %v", err)
			}
		})
	}
}

func TestSubscribe_afterCloseReturnsErrStoreClosed(t *testing.T) {
	for _, op := range subscribeOps {
		t.Run(op.name, func(t *testing.T) {
			s := newTestStore(t)
			s.Close()
			if err := op.subErr(s, context.Background()); !errors.Is(err, store.ErrStoreClosed) {
				t.Fatalf("want ErrStoreClosed, got %v", err)
			}
		})
	}
}

func TestSubscribe_contextCancelClosesChannel(t *testing.T) {
	for _, op := range subscribeOps {
		t.Run(op.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			assertClosed := op.subscribe(s, ctx)
			cancel()
			assertClosed(t)
		})
	}
}

func TestSubChan_send(t *testing.T) {
	sc := newSubChan[int]()
	sc.send(42)
	if sc.Drops() != 0 {
		t.Fatalf("want 0 drops, got %d", sc.Drops())
	}
	select {
	case v := <-sc.C():
		if v != 42 {
			t.Fatalf("want 42, got %d", v)
		}
	default:
		t.Fatal("expected value in channel")
	}
}

func TestSubChan_dropsWhenFull(t *testing.T) {
	sc := newSubChan[int]()
	for range subscriberBufSize {
		sc.send(1)
	}
	sc.send(2)
	if sc.Drops() != 1 {
		t.Fatalf("want 1 drop, got %d", sc.Drops())
	}
}

func TestSubChan_dropsAfterClose(t *testing.T) {
	sc := newSubChan[int]()
	sc.closeOnce()
	sc.send(1)
	if sc.Drops() != 1 {
		t.Fatalf("want 1 drop after close, got %d", sc.Drops())
	}
}

func TestSubChan_closeOnceIdempotent(t *testing.T) {
	sc := newSubChan[int]()
	sc.closeOnce()
	sc.closeOnce()
	sc.closeOnce()
}

func TestSubChan_concurrentSendAndClose(t *testing.T) {
	for range 50 {
		sc := newSubChan[int]()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 100 {
				sc.send(1)
			}
		}()
		go func() {
			defer wg.Done()
			sc.closeOnce()
		}()
		wg.Wait()
		// drain remaining
		for range sc.C() {
		}
	}
}

func TestSubscribeInsights_receivesInsight(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := s.SubscribeInsights(ctx, store.InsightFilter{})

	id := store.NewID()
	if err := s.AddInsight(context.Background(), store.Insight{
		ID: id, ObserverName: "obs", Pattern: "high-cpu",
		Severity: store.SeverityWarning, Summary: "CPU over 90%",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	got, ok := recv(t, ch, time.Second)
	if !ok {
		t.Fatal("timeout")
	}
	if got.ID != id {
		t.Fatalf("id: want %s, got %s", id, got.ID)
	}
	if got.Pattern != "high-cpu" {
		t.Fatalf("pattern: want high-cpu, got %s", got.Pattern)
	}
}

func TestSubscribeInsights_filterBySeverity(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := s.SubscribeInsights(ctx, store.InsightFilter{
		Severities: []store.Severity{store.SeverityCritical},
	})

	s.AddInsight(context.Background(), store.Insight{
		ID: store.NewID(), ObserverName: "o", Pattern: "warn", Severity: store.SeverityWarning,
		Summary: "warning", CreatedAt: time.Now(),
	})
	s.AddInsight(context.Background(), store.Insight{
		ID: store.NewID(), ObserverName: "o", Pattern: "crit", Severity: store.SeverityCritical,
		Summary: "critical!", CreatedAt: time.Now(),
	})

	got, ok := recv(t, ch, time.Second)
	if !ok {
		t.Fatal("timeout: expected critical insight")
	}
	if got.Severity != store.SeverityCritical {
		t.Fatalf("want critical, got %s", got.Severity)
	}
	expectNone(t, ch)
}

func TestQueryByTime_closedStore(t *testing.T) {
	s := newTestStore(t)
	s.Close()
	_, err := s.QueryTelemetryByTime(context.Background(), store.TelemetryTimeRangeQuery{Start: time.Now().Add(-time.Hour)})
	if !errors.Is(err, store.ErrStoreClosed) {
		t.Fatalf("want ErrStoreClosed, got %v", err)
	}
}

func TestQueryByTime_invalidQuery(t *testing.T) {
	s := newTestStore(t)
	_, err := s.QueryTelemetryByTime(context.Background(), store.TelemetryTimeRangeQuery{})
	if !errors.Is(err, store.ErrInvalidQuery) {
		t.Fatalf("want ErrInvalidQuery, got %v", err)
	}
}

func TestQueryByTime_emptyResult(t *testing.T) {
	s := newTestStore(t)
	res, err := s.QueryTelemetryByTime(context.Background(), store.TelemetryTimeRangeQuery{
		Start: time.Now().Add(-time.Hour),
		End:   time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 0 {
		t.Fatalf("want 0 records, got %d", len(res.Records))
	}
}

func TestQueryFromCursor_closedStore(t *testing.T) {
	s := newTestStore(t)
	s.Close()
	_, err := s.QueryTelemetryFromCursor(context.Background(), store.TelemetryCursorQuery{Count: 10})
	if !errors.Is(err, store.ErrStoreClosed) {
		t.Fatalf("want ErrStoreClosed, got %v", err)
	}
}

func TestQueryFromCursor_invalidQuery(t *testing.T) {
	s := newTestStore(t)
	_, err := s.QueryTelemetryFromCursor(context.Background(), store.TelemetryCursorQuery{Count: 0})
	if !errors.Is(err, store.ErrInvalidQuery) {
		t.Fatalf("want ErrInvalidQuery, got %v", err)
	}
}

func TestQueryFromCursor_invalidCursor(t *testing.T) {
	s := newTestStore(t)
	_, err := s.QueryTelemetryFromCursor(context.Background(), store.TelemetryCursorQuery{
		AfterID: "not-a-uuid", Count: 10,
	})
	if !errors.Is(err, store.ErrInvalidID) {
		t.Fatalf("want ErrInvalidID, got %v", err)
	}
}

func TestQueryFromCursor_emptyResult(t *testing.T) {
	s := newTestStore(t)
	res, err := s.QueryTelemetryFromCursor(context.Background(), store.TelemetryCursorQuery{Count: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 0 {
		t.Fatalf("want 0 records, got %d", len(res.Records))
	}
}

func TestConcurrentAddAndSubscribe(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const numSubs = 5
	channels := make([]<-chan store.TelemetryRecord, numSubs)
	for i := range numSubs {
		ch, err := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})
		if err != nil {
			t.Fatal(err)
		}
		channels[i] = ch
	}

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			envs := make([]store.TelemetryRecord, 20)
			for i := range envs {
				envs[i] = makeEnv(nil)
			}
			s.AddTelemetry(context.Background(), envs)
		}()
	}
	wg.Wait()
}

func TestConcurrentCloseAndAdd(t *testing.T) {
	for range 20 {
		s := newTestStore(t)
		ctx := context.Background()
		ch, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 50 {
				if err := s.AddTelemetry(context.Background(), []store.TelemetryRecord{makeEnv(nil)}); err != nil && !errors.Is(err, store.ErrStoreClosed) {
					t.Error(err)
				}
			}
		}()
		go func() {
			defer wg.Done()
			s.Close()
		}()
		wg.Wait()

		// channel must be closeable — drain it
		done := make(chan struct{})
		go func() {
			for range ch {
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("channel not closed after Close()")
		}
	}
}

func TestConcurrentContextCancelAndClose(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())

	ch, _ := s.SubscribeTelemetry(ctx, store.TelemetryFilter{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); cancel() }()
	go func() { defer wg.Done(); s.Close() }()
	wg.Wait()

	// must not deadlock
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("channel not closed")
	}
}

func TestAddActionLog_afterCloseReturnsErrStoreClosed(t *testing.T) {
	s := newTestStore(t)
	s.Close()
	err := s.AddActionLog(context.Background(), store.ActionLog{ExecutionID: "exec-1"})
	if !errors.Is(err, store.ErrStoreClosed) {
		t.Fatalf("want ErrStoreClosed, got %v", err)
	}
}

func TestAddRecordedAction_openStoreReturnsNil(t *testing.T) {
	s := newTestStore(t)
	err := s.AddRecordedAction(context.Background(), store.RecordedAction{ID: store.NewID()})
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestAddActionLog_openStoreReturnsNil(t *testing.T) {
	s := newTestStore(t)
	err := s.AddActionLog(context.Background(), store.ActionLog{ExecutionID: "exec-1"})
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}
