package doris

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/uuidv7"
)

//nolint:gochecknoglobals
var fakeRowsRegistry sync.Map

//nolint:gochecknoinits
func init() {
	sql.Register("fakerows", rowsDriver{})
	sql.Register("failexec", failDriver{})
}

type (
	rowsDriver struct{}
	rowsConn   struct{ dsn string }
	rowsStmt   struct {
		dsn   string
		query string
	}
	cannedRows struct {
		cols []string
		data [][]driver.Value
		pos  int
	}
)

func (rowsDriver) Open(dsn string) (driver.Conn, error) { return &rowsConn{dsn: dsn}, nil }
func (c *rowsConn) Prepare(query string) (driver.Stmt, error) {
	return &rowsStmt{dsn: c.dsn, query: query}, nil
}
func (c *rowsConn) Close() error              { return nil }
func (c *rowsConn) Begin() (driver.Tx, error) { return nil, driver.ErrBadConn }
func (s *rowsStmt) Close() error              { return nil }
func (s *rowsStmt) NumInput() int             { return -1 }
func (s *rowsStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

func (s *rowsStmt) Query(_ []driver.Value) (driver.Rows, error) {
	cols := []string{"id", "signal", "payload", "attributes", "received_at"}
	if !strings.Contains(s.query, "FROM telemetry") {
		return &cannedRows{cols: cols}, nil
	}
	var data [][]driver.Value
	if v, ok := fakeRowsRegistry.Load(s.dsn); ok {
		data = v.([][]driver.Value)
	}
	return &cannedRows{cols: cols, data: data}, nil
}

func (r *cannedRows) Columns() []string { return r.cols }
func (r *cannedRows) Close() error      { return nil }
func (r *cannedRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.pos])
	r.pos++
	return nil
}

type (
	failDriver struct{}
	failConn   struct{}
	failStmt   struct{}
)

var errFakeExec = errors.New("fakerows: exec failed")

func (failDriver) Open(_ string) (driver.Conn, error)     { return &failConn{}, nil }
func (c *failConn) Prepare(_ string) (driver.Stmt, error) { return &failStmt{}, nil }
func (c *failConn) Close() error                          { return nil }
func (c *failConn) Begin() (driver.Tx, error)             { return nil, driver.ErrBadConn }
func (s *failStmt) Close() error                          { return nil }
func (s *failStmt) NumInput() int                         { return -1 }
func (s *failStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return nil, errFakeExec
}
func (s *failStmt) Query(_ []driver.Value) (driver.Rows, error) {
	return nil, errFakeExec
}

func row(id, signal, payload, attrs string) []driver.Value {
	return []driver.Value{id, signal, payload, attrs, time.Time{}}
}

func newRowStore(t *testing.T, rows [][]driver.Value) *DorisStore {
	t.Helper()
	dsn := t.Name()
	fakeRowsRegistry.Store(dsn, rows)
	t.Cleanup(func() { fakeRowsRegistry.Delete(dsn) })

	db, err := sql.Open("fakerows", dsn)
	if err != nil {
		t.Fatalf("open fakerows db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s, err := NewStore(db, Params{}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestNewDorisStore_success(t *testing.T) {
	db, err := sql.Open("fakerows", "new-store-ok")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s, err := NewStore(db, Params{}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if s.subs == nil || s.insightSubs == nil {
		t.Fatal("subscriber maps not initialised")
	}
}

func TestNewDorisStore_schemaError(t *testing.T) {
	db, err := sql.Open("failexec", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = NewStore(db, Params{}, nil)
	if err == nil {
		t.Fatal("expected error from failing DDL")
	}
}

func TestQueryByTime_returnsRows(t *testing.T) {
	id1 := uuidv7.Generate()
	id2 := uuidv7.Generate()
	s := newRowStore(t, [][]driver.Value{
		row(id1, "metrics", `{"a":1}`, `{"service.name":"nginx"}`),
		row(id2, "logs", `{"b":2}`, `{"service.name":"payment"}`),
	})

	res, err := s.QueryTelemetryByTime(context.Background(), store.TelemetryTimeRangeQuery{
		Start: time.Now().Add(-time.Hour),
		End:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("want 2 records, got %d", len(res.Records))
	}
	if res.Records[0].Signal != store.SignalMetrics {
		t.Fatalf("signal: want metrics, got %s", res.Records[0].Signal)
	}
	if string(res.Records[0].Payload) != `{"a":1}` {
		t.Fatalf("payload mismatch: %s", res.Records[0].Payload)
	}
}

func TestQueryByTime_attributeFilter(t *testing.T) {
	id1 := uuidv7.Generate()
	id2 := uuidv7.Generate()
	s := newRowStore(t, [][]driver.Value{
		row(id1, "metrics", `{}`, `{"service.name":"nginx"}`),
		row(id2, "metrics", `{}`, `{"service.name":"payment"}`),
	})

	res, err := s.QueryTelemetryByTime(context.Background(), store.TelemetryTimeRangeQuery{
		Start:      time.Now().Add(-time.Hour),
		Attributes: map[string]string{"service.name": "nginx"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 {
		t.Fatalf("want 1 filtered record, got %d", len(res.Records))
	}
	if res.Records[0].Attributes["service.name"] != "nginx" {
		t.Fatalf("want nginx, got %s", res.Records[0].Attributes["service.name"])
	}
}

func TestQueryByTime_startOnly(t *testing.T) {
	s := newRowStore(t, nil)
	_, err := s.QueryTelemetryByTime(context.Background(), store.TelemetryTimeRangeQuery{
		Start: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestQueryByTime_endOnly(t *testing.T) {
	s := newRowStore(t, nil)
	_, err := s.QueryTelemetryByTime(context.Background(), store.TelemetryTimeRangeQuery{
		End: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestQueryByTime_queryError(t *testing.T) {
	db, _ := sql.Open("failexec", "")
	t.Cleanup(func() { _ = db.Close() })
	s := &DorisStore{
		subs:        make(map[*subscriber]struct{}),
		insightSubs: make(map[*insightSubscriber]struct{}),
		db:          db,
		log:         slog.Default(),
	}
	_, err := s.QueryTelemetryByTime(context.Background(), store.TelemetryTimeRangeQuery{Start: time.Now()})
	if err == nil {
		t.Fatal("expected query error")
	}
}

func TestQueryFromCursor_returnsRows(t *testing.T) {
	id1 := uuidv7.Generate()
	id2 := uuidv7.Generate()
	s := newRowStore(t, [][]driver.Value{
		row(id1, "metrics", `{}`, `{}`),
		row(id2, "metrics", `{}`, `{}`),
	})

	res, err := s.QueryTelemetryFromCursor(context.Background(), store.TelemetryCursorQuery{Count: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("want 2 records, got %d", len(res.Records))
	}
	if res.NextCursor != "" {
		t.Fatalf("want empty cursor (no more), got %s", res.NextCursor)
	}
}

func TestQueryFromCursor_hasMore(t *testing.T) {
	id1 := uuidv7.Generate()
	id2 := uuidv7.Generate()
	id3 := uuidv7.Generate()
	// Count=2 but driver returns 3 rows → hasMore, cursor set
	s := newRowStore(t, [][]driver.Value{
		row(id1, "metrics", `{}`, `{}`),
		row(id2, "metrics", `{}`, `{}`),
		row(id3, "metrics", `{}`, `{}`),
	})

	res, err := s.QueryTelemetryFromCursor(context.Background(), store.TelemetryCursorQuery{Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("want 2 records (trimmed), got %d", len(res.Records))
	}
	if res.NextCursor != store.RecordID(id2) {
		t.Fatalf("cursor: want %s, got %s", id2, res.NextCursor)
	}
}

func TestQueryFromCursor_withAfterID(t *testing.T) {
	after := uuidv7.Generate()
	next := uuidv7.Generate()
	s := newRowStore(t, [][]driver.Value{
		row(next, "metrics", `{}`, `{}`),
	})

	res, err := s.QueryTelemetryFromCursor(context.Background(), store.TelemetryCursorQuery{
		AfterID: store.RecordID(after),
		Count:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 {
		t.Fatalf("want 1 record, got %d", len(res.Records))
	}
}

func TestQueryFromCursor_attributeFilter(t *testing.T) {
	id1 := uuidv7.Generate()
	id2 := uuidv7.Generate()
	s := newRowStore(t, [][]driver.Value{
		row(id1, "metrics", `{}`, `{"env":"prod"}`),
		row(id2, "metrics", `{}`, `{"env":"dev"}`),
	})

	res, err := s.QueryTelemetryFromCursor(context.Background(), store.TelemetryCursorQuery{
		Count:      10,
		Attributes: map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 {
		t.Fatalf("want 1 filtered record, got %d", len(res.Records))
	}
}

func TestQueryFromCursor_queryError(t *testing.T) {
	db, _ := sql.Open("failexec", "")
	t.Cleanup(func() { _ = db.Close() })
	s := &DorisStore{
		subs:        make(map[*subscriber]struct{}),
		insightSubs: make(map[*insightSubscriber]struct{}),
		db:          db,
		log:         slog.Default(),
	}
	_, err := s.QueryTelemetryFromCursor(context.Background(), store.TelemetryCursorQuery{Count: 10})
	if err == nil {
		t.Fatal("expected query error")
	}
}

func TestQueryByTime_malformedAttributes(t *testing.T) {
	id := uuidv7.Generate()
	s := newRowStore(t, [][]driver.Value{
		row(id, "metrics", `{}`, `not-valid-json`),
	})

	res, err := s.QueryTelemetryByTime(context.Background(), store.TelemetryTimeRangeQuery{
		Start: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 {
		t.Fatalf("want 1 record, got %d", len(res.Records))
	}
	if res.Records[0].Attributes == nil || len(res.Records[0].Attributes) != 0 {
		t.Fatalf("want empty attributes map, got %v", res.Records[0].Attributes)
	}
}

func TestWriteToDoris_errorBranches(t *testing.T) {
	db, _ := sql.Open("failexec", "")
	t.Cleanup(func() { _ = db.Close() })
	s := &DorisStore{db: db, log: slog.Default()}

	// These must not panic; they log the exec error and return it.
	_ = s.flushTelemetry(context.Background(), []store.TelemetryRecord{
		{ID: store.RecordID(uuidv7.Generate()), Signal: store.SignalMetrics},
	})
	_ = s.flushInsights(context.Background(), []store.Insight{
		{ID: store.RecordID(uuidv7.Generate()), CreatedAt: time.Now(), SourceRecordIDs: []store.RecordID{"a", "b"}},
	})
	_ = s.flushRecordedActions(context.Background(), []store.RecordedAction{
		{ID: store.RecordID(uuidv7.Generate()), Timestamp: time.Now()},
	})
	_ = s.flushActionLogs(context.Background(), []store.ActionLog{
		{ID: store.RecordID(uuidv7.Generate()), ExecutionID: "exec-1", Timestamp: time.Now()},
	})
}

func TestAddInsight_createdAtPreserved(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, _ := s.SubscribeInsights(ctx, store.InsightFilter{})

	want := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	err := s.AddInsight(context.Background(), store.Insight{
		ID: "i", ObserverName: "o", Pattern: "p", Severity: store.SeverityInfo, Summary: "s",
		CreatedAt: want,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, ok := recv(t, ch, time.Second)
	if !ok {
		t.Fatal("timeout waiting for insight")
	}
	if !got.CreatedAt.Equal(want) {
		t.Fatalf("want CreatedAt %v, got %v", want, got.CreatedAt)
	}
}

// rowsIterErrDriver returns a non-io.EOF error from rows.Next, exercising the rows.Err() check.
type (
	rowsIterErrDriver  struct{}
	rowsIterErrConn    struct{}
	rowsIterErrStmt    struct{}
	rowsIterErrRowsObj struct{}
)

var errRowsIterator = errors.New("rows: iterator error")

//nolint:gochecknoinits
func init() {
	sql.Register("rowsiterr", rowsIterErrDriver{})
}

func (rowsIterErrDriver) Open(_ string) (driver.Conn, error)   { return &rowsIterErrConn{}, nil }
func (*rowsIterErrConn) Prepare(_ string) (driver.Stmt, error) { return &rowsIterErrStmt{}, nil }
func (*rowsIterErrConn) Close() error                          { return nil }
func (*rowsIterErrConn) Begin() (driver.Tx, error)             { return nil, driver.ErrBadConn }
func (*rowsIterErrStmt) Close() error                          { return nil }
func (*rowsIterErrStmt) NumInput() int                         { return -1 }
func (*rowsIterErrStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (*rowsIterErrStmt) Query(_ []driver.Value) (driver.Rows, error) {
	return &rowsIterErrRowsObj{}, nil
}
func (*rowsIterErrRowsObj) Columns() []string {
	return []string{"id", "signal", "payload", "attributes", "received_at"}
}
func (*rowsIterErrRowsObj) Close() error                { return nil }
func (*rowsIterErrRowsObj) Next(_ []driver.Value) error { return errRowsIterator }

func newRowsIterErrStore(t *testing.T) *DorisStore {
	t.Helper()
	db, err := sql.Open("rowsiterr", "")
	if err != nil {
		t.Fatalf("open rowsiterr db: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	return &DorisStore{
		subs:        make(map[*subscriber]struct{}),
		insightSubs: make(map[*insightSubscriber]struct{}),
		db:          db,
	}
}

func TestQueryByTime_rowsIteratorError(t *testing.T) {
	s := newRowsIterErrStore(t)
	_, err := s.QueryTelemetryByTime(context.Background(), store.TelemetryTimeRangeQuery{
		Start: time.Now().Add(-time.Hour),
	})
	if err == nil {
		t.Fatal("expected rows iterator error")
	}
}

func TestQueryFromCursor_rowsIteratorError(t *testing.T) {
	s := newRowsIterErrStore(t)
	_, err := s.QueryTelemetryFromCursor(context.Background(), store.TelemetryCursorQuery{Count: 10})
	if err == nil {
		t.Fatal("expected rows iterator error")
	}
}

// scanErrDriver returns a row with fewer columns than scanTelemetry expects (3 vs 5),
// so rows.Scan returns "expected N destination arguments" and exercises the scan-error path.
type (
	scanErrDriver  struct{}
	scanErrConn    struct{}
	scanErrStmt    struct{}
	scanErrRowsObj struct{ done bool }
)

//nolint:gochecknoinits
func init() {
	sql.Register("scanerr", scanErrDriver{})
}

func (scanErrDriver) Open(_ string) (driver.Conn, error)   { return &scanErrConn{}, nil }
func (*scanErrConn) Prepare(_ string) (driver.Stmt, error) { return &scanErrStmt{}, nil }
func (*scanErrConn) Close() error                          { return nil }
func (*scanErrConn) Begin() (driver.Tx, error)             { return nil, driver.ErrBadConn }
func (*scanErrStmt) Close() error                          { return nil }
func (*scanErrStmt) NumInput() int                         { return -1 }
func (*scanErrStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (*scanErrStmt) Query(_ []driver.Value) (driver.Rows, error) { return &scanErrRowsObj{}, nil }
func (r *scanErrRowsObj) Columns() []string                      { return []string{"id", "signal", "payload"} }
func (r *scanErrRowsObj) Close() error                           { return nil }
func (r *scanErrRowsObj) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0], dest[1], dest[2] = "id-val", "metrics", "{}"
	return nil
}

func TestQueryByTime_scanError(t *testing.T) {
	db, _ := sql.Open("scanerr", "")
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	s := &DorisStore{
		subs:        make(map[*subscriber]struct{}),
		insightSubs: make(map[*insightSubscriber]struct{}),
		db:          db,
	}
	_, err := s.QueryTelemetryByTime(context.Background(), store.TelemetryTimeRangeQuery{
		Start: time.Now().Add(-time.Hour),
	})
	if err == nil {
		t.Fatal("expected scan error")
	}
}

func TestQueryFromCursor_scanError(t *testing.T) {
	db, _ := sql.Open("scanerr", "")
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	s := &DorisStore{
		subs:        make(map[*subscriber]struct{}),
		insightSubs: make(map[*insightSubscriber]struct{}),
		db:          db,
	}
	_, err := s.QueryTelemetryFromCursor(context.Background(), store.TelemetryCursorQuery{Count: 10})
	if err == nil {
		t.Fatal("expected scan error")
	}
}
