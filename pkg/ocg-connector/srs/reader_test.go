package srs

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// --- fake database/sql driver -------------------------------------------------

//nolint:gochecknoglobals // test-only registry keyed by DSN
var fakeReg sync.Map

type capture struct {
	query string
	args  []driver.Value
}

type (
	fakeDriver struct{}
	fakeConn   struct{ dsn string }
	fakeStmt   struct {
		dsn   string
		query string
	}
	fakeRows struct {
		cols    []string
		data    [][]driver.Value
		pos     int
		rowsErr error
	}
)

//nolint:gochecknoinits // registers the test driver
func init() { sql.Register("srsfake", fakeDriver{}) }

func (fakeDriver) Open(dsn string) (driver.Conn, error) { return &fakeConn{dsn: dsn}, nil }
func (c *fakeConn) Prepare(q string) (driver.Stmt, error) {
	return &fakeStmt{dsn: c.dsn, query: q}, nil
}
func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return nil, driver.ErrBadConn }
func (s *fakeStmt) Close() error              { return nil }
func (s *fakeStmt) NumInput() int             { return -1 }
func (s *fakeStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	v, _ := fakeReg.Load(s.dsn)
	st := v.(*fakeState)
	st.last = &capture{query: s.query, args: args}
	if st.err != nil {
		return nil, st.err
	}
	for table, rows := range st.rows {
		if strings.Contains(s.query, "FROM "+table) {
			return &fakeRows{cols: st.cols[table], data: rows, rowsErr: st.rowsErr}, nil
		}
	}
	return &fakeRows{}, nil
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.data) {
		if r.rowsErr != nil {
			return r.rowsErr
		}
		return io.EOF
	}
	copy(dest, r.data[r.pos])
	r.pos++
	return nil
}

type fakeState struct {
	rows    map[string][][]driver.Value
	cols    map[string][]string
	err     error
	rowsErr error
	last    *capture
}

func newReader(t *testing.T, st *fakeState) *Reader {
	t.Helper()
	dsn := t.Name()
	fakeReg.Store(dsn, st)
	t.Cleanup(func() { fakeReg.Delete(dsn) })
	db, err := sql.Open("srsfake", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewReader(db)
}

// telCols is the column list for the telemetry table including schema_version.
//
//nolint:gochecknoglobals // test fixture shared across subtests
var telCols = []string{"id", "signal", "payload", "attributes", "received_at", "schema_version"}

// --- tests --------------------------------------------------------------------

func TestFetchTelemetryMappingAndQueryShape(t *testing.T) {
	st := &fakeState{
		cols: map[string][]string{"telemetry": telCols},
		rows: map[string][][]driver.Value{"telemetry": {
			{"01934ab2-1234-7000-8000-000000000001", "metrics", `{"collector":"cpu","value":0.43}`, `{}`, "2024-11-01 10:00:00.100", "1.0.0"},
		}},
	}
	r := newReader(t, st)

	const since = "01934ab2-0000-7000-8000-000000000000"
	recs, err := r.Fetch(context.Background(), StoreTelemetry, since, "", 100)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	rec := recs[0]
	if rec.ScopeName != "satellite/srs/telemetry" {
		t.Errorf("scope = %q", rec.ScopeName)
	}
	if rec.TimeUnixNano == 0 {
		t.Error("TimeUnixNano not set")
	}
	if rec.SchemaVersion != "1.0.0" {
		t.Errorf("SchemaVersion = %q, want %q", rec.SchemaVersion, "1.0.0")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body, &body); err != nil {
		t.Fatalf("body not valid JSON: %v (%s)", err, rec.Body)
	}
	if string(body["id"]) != `"01934ab2-1234-7000-8000-000000000001"` {
		t.Errorf("body.id = %s", body["id"])
	}
	if string(body["signal"]) != `"metrics"` {
		t.Errorf("body.signal = %s", body["signal"])
	}
	// payload is []byte → base64 in JSON
	if _, ok := body["payload"]; !ok {
		t.Error("body missing payload")
	}

	q := st.last.query
	for _, want := range []string{"FROM telemetry", "WHERE id > ?", "ORDER BY id ASC", "LIMIT 100"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q missing %q", q, want)
		}
	}
	if len(st.last.args) != 1 || st.last.args[0] != since {
		t.Errorf("args = %v, want [%q]", st.last.args, since)
	}
}

func TestFetchTelemetryAttributesFlattened(t *testing.T) {
	st := &fakeState{
		cols: map[string][]string{"telemetry": telCols},
		rows: map[string][][]driver.Value{"telemetry": {
			{"01934ab2-1234-7000-8000-000000000001", "metrics", "",
				`{"source":"cpu-temp","value":"74.3","nested":{"unit":"celsius"}}`, "2024-11-01 10:00:00", ""},
		}},
	}
	r := newReader(t, st)
	recs, err := r.Fetch(context.Background(), StoreTelemetry, "", "", 10)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var body struct {
		Attributes map[string]string `json:"attributes"`
	}
	if err := json.Unmarshal(recs[0].Body, &body); err != nil {
		t.Fatalf("body not valid JSON: %v (%s)", err, recs[0].Body)
	}
	want := map[string]string{"source": "cpu-temp", "value": "74.3", "nested.unit": "celsius"}
	for k, v := range want {
		if body.Attributes[k] != v {
			t.Errorf("attributes[%q] = %q, want %q (got %v)", k, body.Attributes[k], v, body.Attributes)
		}
	}
}

func TestFetchEmptyCursorOmitsWhere(t *testing.T) {
	st := &fakeState{
		cols: map[string][]string{"telemetry": telCols},
		rows: map[string][][]driver.Value{"telemetry": {}},
	}
	r := newReader(t, st)
	if _, err := r.Fetch(context.Background(), StoreTelemetry, "", "", 50); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if strings.Contains(st.last.query, "WHERE") {
		t.Errorf("query should omit WHERE: %q", st.last.query)
	}
	if len(st.last.args) != 0 {
		t.Errorf("args = %v, want empty", st.last.args)
	}
}

func TestFetchSchemaVersionFilter(t *testing.T) {
	st := &fakeState{
		cols: map[string][]string{"telemetry": telCols},
		rows: map[string][][]driver.Value{"telemetry": {
			{"01934ab2-1234-7000-8000-000000000001", "metrics", "{}", `{}`, "2024-11-01 10:00:00", "1.0.0"},
		}},
	}
	r := newReader(t, st)
	recs, err := r.Fetch(context.Background(), StoreTelemetry, "", "1.0.0", 10)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	q := st.last.query
	if !strings.Contains(q, "WHERE schema_version = ?") {
		t.Errorf("query missing WHERE schema_version = ?: %q", q)
	}
	if len(st.last.args) != 1 || st.last.args[0] != "1.0.0" {
		t.Errorf("args = %v, want [\"1.0.0\"]", st.last.args)
	}
}

func TestFetchSchemaVersionAndCursorFilter(t *testing.T) {
	st := &fakeState{
		cols: map[string][]string{"telemetry": telCols},
		rows: map[string][][]driver.Value{"telemetry": {}},
	}
	r := newReader(t, st)
	const since = "01934ab2-0000-7000-8000-000000000000"
	if _, err := r.Fetch(context.Background(), StoreTelemetry, since, "1.0.0", 10); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	q := st.last.query
	if !strings.Contains(q, "WHERE id > ?") || !strings.Contains(q, "AND schema_version = ?") {
		t.Errorf("query = %q, want both id and schema_version conditions", q)
	}
	if len(st.last.args) != 2 || st.last.args[0] != since || st.last.args[1] != "1.0.0" {
		t.Errorf("args = %v, want [%q, %q]", st.last.args, since, "1.0.0")
	}
}

func TestFetchInsightBody(t *testing.T) {
	st := &fakeState{
		cols: map[string][]string{"insights": {
			"id", "observer_name", "pattern", "severity", "summary",
			"window_start", "window_end", "created_at", "source_record_ids", "schema_version",
		}},
		rows: map[string][][]driver.Value{"insights": {{
			"01934ab2-1234-7000-8000-000000000002", "obs", "pat", "warning", "sum",
			"2024-11-01 10:00:00", "2024-11-01 10:05:00", "2024-11-01 10:06:00.500",
			`["01934ab2-1234-7000-8000-000000000001"]`, "1.0.0",
		}}},
	}
	r := newReader(t, st)
	recs, err := r.Fetch(context.Background(), StoreInsights, "", "", 10)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(recs[0].Body, &body); err != nil {
		t.Fatalf("body not valid JSON: %v (%s)", err, recs[0].Body)
	}
	if string(body["id"]) != `"01934ab2-1234-7000-8000-000000000002"` {
		t.Errorf("body.id = %s", body["id"])
	}
	if string(body["observer_name"]) != `"obs"` {
		t.Errorf("observer_name = %s", body["observer_name"])
	}
	if string(body["source_record_ids"]) != `["01934ab2-1234-7000-8000-000000000001"]` {
		t.Errorf("source_record_ids = %s", body["source_record_ids"])
	}
	if _, ok := body["created_at"]; !ok {
		t.Error("body missing created_at")
	}
	if recs[0].ScopeName != "satellite/srs/insight" {
		t.Errorf("scope = %q", recs[0].ScopeName)
	}
	if recs[0].SchemaVersion != "1.0.0" {
		t.Errorf("SchemaVersion = %q, want %q", recs[0].SchemaVersion, "1.0.0")
	}
}

func TestFetchActionLogAndRecordedAction(t *testing.T) {
	st := &fakeState{
		cols: map[string][]string{
			"action_logs":      {"id", "execution_id", "step_id", "description", "state", "timestamp", "schema_version"},
			"recorded_actions": {"id", "execution_id", "insight_id", "operator_name", "state", "timestamp", "schema_version"},
		},
		rows: map[string][][]driver.Value{
			"action_logs": {{
				"01934ab2-1234-7000-8000-000000000003", "01934ab2-1234-7000-8000-00000000000e",
				"step1", "did a thing", "success", "2024-11-01 10:00:00", "1.0.0",
			}},
			"recorded_actions": {{
				"01934ab2-1234-7000-8000-000000000004", "01934ab2-1234-7000-8000-00000000000e",
				"01934ab2-1234-7000-8000-00000000000f", "op", "started", "2024-11-01 10:00:01", "1.0.0",
			}},
		},
	}
	r := newReader(t, st)

	al, err := r.Fetch(context.Background(), StoreActionLogs, "", "", 10)
	if err != nil {
		t.Fatalf("action_logs: %v", err)
	}
	if al[0].ScopeName != "satellite/srs/actionlog" || !strings.Contains(string(al[0].Body), `"step_id":"step1"`) {
		t.Errorf("action log body = %s scope = %s", al[0].Body, al[0].ScopeName)
	}
	if !strings.Contains(string(al[0].Body), `"id":"01934ab2-1234-7000-8000-000000000003"`) {
		t.Errorf("action log body missing id: %s", al[0].Body)
	}
	if !strings.Contains(string(al[0].Body), `"timestamp"`) {
		t.Errorf("action log body missing timestamp: %s", al[0].Body)
	}

	ra, err := r.Fetch(context.Background(), StoreRecordedActions, "", "", 10)
	if err != nil {
		t.Fatalf("recorded_actions: %v", err)
	}
	if ra[0].ScopeName != "satellite/srs/recordedaction" || !strings.Contains(string(ra[0].Body), `"operator_name":"op"`) {
		t.Errorf("recorded action body = %s scope = %s", ra[0].Body, ra[0].ScopeName)
	}
	if !strings.Contains(string(ra[0].Body), `"id":"01934ab2-1234-7000-8000-000000000004"`) {
		t.Errorf("recorded action body missing id: %s", ra[0].Body)
	}
	if !strings.Contains(string(ra[0].Body), `"timestamp"`) {
		t.Errorf("recorded action body missing timestamp: %s", ra[0].Body)
	}
}

func TestFetchUnknownStore(t *testing.T) {
	r := newReader(t, &fakeState{})
	_, err := r.Fetch(context.Background(), "nope", "", "", 10)
	if !errors.Is(err, ErrUnknownStore) {
		t.Fatalf("err = %v, want ErrUnknownStore", err)
	}
}

func TestFetchInvalidCursor(t *testing.T) {
	r := newReader(t, &fakeState{})
	_, err := r.Fetch(context.Background(), StoreTelemetry, "not-a-uuid", "", 10)
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("err = %v, want ErrInvalidCursor", err)
	}
}

func TestFetchQueryError(t *testing.T) {
	r := newReader(t, &fakeState{err: errors.New("boom")})
	_, err := r.Fetch(context.Background(), StoreTelemetry, "", "", 10)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFetchBadTimestamp(t *testing.T) {
	st := &fakeState{
		cols: map[string][]string{"telemetry": telCols},
		rows: map[string][][]driver.Value{"telemetry": {
			{"01934ab2-1234-7000-8000-000000000001", "metrics", "{}", `{}`, "not-a-time", ""},
		}},
	}
	r := newReader(t, st)
	if _, err := r.Fetch(context.Background(), StoreTelemetry, "", "", 10); err == nil {
		t.Fatal("expected parse error")
	}
}

// badTimestampCases exercises every parseTime branch across the scanners.
func TestFetchBadTimestampAllStores(t *testing.T) {
	cases := []struct {
		name  string
		store string
		cols  []string
		row   []driver.Value
	}{
		{"insight window_start", StoreInsights,
			[]string{"id", "observer_name", "pattern", "severity", "summary", "window_start", "window_end", "created_at", "source_record_ids", "schema_version"},
			[]driver.Value{"01934ab2-1234-7000-8000-000000000002", "o", "p", "info", "s", "bad", "2024-11-01 10:00:00", "2024-11-01 10:00:00", "", ""}},
		{"insight window_end", StoreInsights,
			[]string{"id", "observer_name", "pattern", "severity", "summary", "window_start", "window_end", "created_at", "source_record_ids", "schema_version"},
			[]driver.Value{"01934ab2-1234-7000-8000-000000000002", "o", "p", "info", "s", "2024-11-01 10:00:00", "bad", "2024-11-01 10:00:00", "", ""}},
		{"insight created_at", StoreInsights,
			[]string{"id", "observer_name", "pattern", "severity", "summary", "window_start", "window_end", "created_at", "source_record_ids", "schema_version"},
			[]driver.Value{"01934ab2-1234-7000-8000-000000000002", "o", "p", "info", "s", "2024-11-01 10:00:00", "2024-11-01 10:00:00", "bad", "", ""}},
		{"action log timestamp", StoreActionLogs,
			[]string{"id", "execution_id", "step_id", "description", "state", "timestamp", "schema_version"},
			[]driver.Value{"01934ab2-1234-7000-8000-000000000003", "e", "s", "d", "info", "bad", ""}},
		{"recorded action timestamp", StoreRecordedActions,
			[]string{"id", "execution_id", "insight_id", "operator_name", "state", "timestamp", "schema_version"},
			[]driver.Value{"01934ab2-1234-7000-8000-000000000004", "e", "i", "o", "started", "bad", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table := specs[tc.store].table
			st := &fakeState{
				cols: map[string][]string{table: tc.cols},
				rows: map[string][][]driver.Value{table: {tc.row}},
			}
			r := newReader(t, st)
			if _, err := r.Fetch(context.Background(), tc.store, "", "", 10); err == nil {
				t.Fatalf("%s: expected parse error", tc.name)
			}
		})
	}
}

// TestFetchScanError feeds a NULL into a non-nullable string column, which the
// database/sql layer rejects during Scan.
func TestFetchScanError(t *testing.T) {
	cases := []struct {
		store string
		cols  []string
		row   []driver.Value
	}{
		{StoreTelemetry, telCols,
			[]driver.Value{nil, "metrics", "{}", `{}`, "2024-11-01 10:00:00", ""}},
		{StoreInsights, []string{"id", "observer_name", "pattern", "severity", "summary", "window_start", "window_end", "created_at", "source_record_ids", "schema_version"},
			[]driver.Value{nil, "o", "p", "i", "s", "2024-11-01 10:00:00", "2024-11-01 10:00:00", "2024-11-01 10:00:00", "[]", ""}},
		{StoreActionLogs, []string{"id", "execution_id", "step_id", "description", "state", "timestamp", "schema_version"},
			[]driver.Value{nil, "e", "s", "d", "info", "2024-11-01 10:00:00", ""}},
		{StoreRecordedActions, []string{"id", "execution_id", "insight_id", "operator_name", "state", "timestamp", "schema_version"},
			[]driver.Value{nil, "e", "i", "o", "started", "2024-11-01 10:00:00", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.store, func(t *testing.T) {
			table := specs[tc.store].table
			st := &fakeState{
				cols: map[string][]string{table: tc.cols},
				rows: map[string][][]driver.Value{table: {tc.row}},
			}
			r := newReader(t, st)
			if _, err := r.Fetch(context.Background(), tc.store, "", "", 10); err == nil {
				t.Fatalf("%s: expected scan error", tc.store)
			}
		})
	}
}

// TestFetchRowsErr makes Next return a non-EOF error, exercising rows.Err().
func TestFetchRowsErr(t *testing.T) {
	st := &fakeState{
		cols:    map[string][]string{"telemetry": telCols},
		rows:    map[string][][]driver.Value{"telemetry": {}},
		rowsErr: errors.New("row iteration failed"),
	}
	r := newReader(t, st)
	if _, err := r.Fetch(context.Background(), StoreTelemetry, "", "", 10); err == nil {
		t.Fatal("expected rows error")
	}
}

func TestRawOrNull(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "[]"},
		{`["a","b"]`, `["a","b"]`},
		{"not json", `"not json"`},
	}
	for _, tc := range cases {
		if got := string(rawOrNull(tc.in)); got != tc.want {
			t.Errorf("rawOrNull(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStoresAndHandles(t *testing.T) {
	if len(Stores()) != 4 {
		t.Errorf("Stores() = %v", Stores())
	}
	if !Handles(StoreTelemetry) || Handles("logs") {
		t.Error("Handles mismatch")
	}
}

func TestUnixNanoNegative(t *testing.T) {
	// Times before the Unix epoch clamp to 0.
	pre, err := parseTime("1960-01-01 00:00:00")
	if err != nil {
		t.Fatalf("parseTime: %v", err)
	}
	if got := unixNano(pre); got != 0 {
		t.Errorf("unixNano = %d, want 0", got)
	}
}
