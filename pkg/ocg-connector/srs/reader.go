// Package srs reads Satellite Record Store records from Apache Doris for the
// OCG connector.
package srs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/uuidv7"
)

const (
	StoreTelemetry       = "srs/satellite/telemetry"
	StoreInsights        = "srs/satellite/insights"
	StoreActionLogs      = "srs/satellite/action_logs"
	StoreRecordedActions = "srs/satellite/recorded_actions"
)

var ErrUnknownStore = errors.New("srs: unknown store")

var ErrInvalidCursor = errors.New("srs: invalid cursor (expected UUIDv7)")

// Record is a transport-neutral SRS record ready for OTLP encoding.
type Record struct {
	// ID is the record's UUIDv7, used as the cursor key.
	ID string
	// ScopeName is the OTLP InstrumentationScope.Name discriminator.
	ScopeName     string
	TimeUnixNano  uint64
	Body          []byte
	SchemaVersion string
}

//nolint:gochecknoglobals // immutable lookup table shared by the scanners
var dtLayouts = []string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range dtLayouts {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("srs: parse datetime %q: unrecognised format", s)
}

func unixNano(t time.Time) uint64 {
	n := t.UnixNano()
	if n < 0 {
		return 0
	}
	return uint64(n)
}

type spec struct {
	table     string
	scopeName string
	columns   string
	scan      func(rows *sql.Rows) (Record, error)
}

//nolint:gochecknoglobals // immutable lookup table keyed by store name
var specs = map[string]spec{
	StoreTelemetry: {
		table:     "telemetry",
		scopeName: "satellite/srs/telemetry",
		columns:   "id, signal, payload, attributes, received_at, schema_version",
		scan:      scanTelemetry,
	},
	StoreInsights: {
		table:     "insights",
		scopeName: "satellite/srs/insight",
		columns:   "id, observer_name, pattern, severity, summary, window_start, window_end, created_at, source_record_ids, schema_version",
		scan:      scanInsight,
	},
	StoreActionLogs: {
		table:     "action_logs",
		scopeName: "satellite/srs/actionlog",
		columns:   "id, execution_id, step_id, description, state, timestamp, schema_version",
		scan:      scanActionLog,
	},
	StoreRecordedActions: {
		table:     "recorded_actions",
		scopeName: "satellite/srs/recordedaction",
		columns:   "id, execution_id, insight_id, operator_name, state, timestamp, schema_version",
		scan:      scanRecordedAction,
	},
}

func Stores() []string {
	return []string{StoreTelemetry, StoreInsights, StoreActionLogs, StoreRecordedActions}
}

func Handles(name string) bool {
	_, ok := specs[name]
	return ok
}

type Reader struct {
	db *sql.DB
}

func NewReader(db *sql.DB) *Reader {
	return &Reader{db: db}
}

// Fetch returns up to limit records in store whose UUIDv7 ID is greater than
// sinceID, ordered ascending by ID. When schemaVersion is non-empty only
// records matching that version are returned.
func (r *Reader) Fetch(ctx context.Context, store, sinceID, schemaVersion string, limit int) ([]Record, error) {
	sp, ok := specs[store]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownStore, store)
	}
	if sinceID != "" && !uuidv7.IsValid(sinceID) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidCursor, sinceID)
	}

	// Table and column names come from the internal spec, not from input.
	query := "SELECT " + sp.columns + " FROM " + sp.table //nolint:gosec // G202: identifiers are internal constants, not input
	var where []string
	var args []any
	if sinceID != "" {
		where = append(where, "id > ?")
		args = append(args, sinceID)
	}
	if schemaVersion != "" {
		where = append(where, "schema_version = ?")
		args = append(args, schemaVersion)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += fmt.Sprintf(" ORDER BY id ASC LIMIT %d", limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("srs: query %s: %w", sp.table, err)
	}
	defer func() { _ = rows.Close() }()

	records := make([]Record, 0, limit)
	for rows.Next() {
		rec, scanErr := sp.scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		rec.ScopeName = sp.scopeName
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("srs: rows %s: %w", sp.table, err)
	}
	return records, nil
}

func scanTelemetry(rows *sql.Rows) (Record, error) {
	var id, signal, payload, attrsJSON, receivedAt, schemaVersion string
	if err := rows.Scan(&id, &signal, &payload, &attrsJSON, &receivedAt, &schemaVersion); err != nil {
		return Record{}, fmt.Errorf("srs: scan telemetry: %w", err)
	}
	t, err := parseTime(receivedAt)
	if err != nil {
		return Record{}, err
	}
	body, err := json.Marshal(store.TelemetryRecord{
		ID:         store.RecordID(id),
		Signal:     store.Signal(signal),
		Payload:    []byte(payload),
		Attributes: flattenAttrs(attrsJSON),
		ReceivedAt: t,
	})
	if err != nil {
		return Record{}, fmt.Errorf("srs: marshal telemetry: %w", err)
	}
	return Record{ID: id, TimeUnixNano: unixNano(t), Body: body, SchemaVersion: schemaVersion}, nil
}

func scanInsight(rows *sql.Rows) (Record, error) {
	var (
		id, observerName, pattern, severity, summary string
		windowStart, windowEnd, createdAt            string
		sourceRecordIDs, schemaVersion               string
	)
	if err := rows.Scan(&id, &observerName, &pattern, &severity, &summary,
		&windowStart, &windowEnd, &createdAt, &sourceRecordIDs, &schemaVersion); err != nil {
		return Record{}, fmt.Errorf("srs: scan insight: %w", err)
	}
	ws, err := parseTime(windowStart)
	if err != nil {
		return Record{}, err
	}
	we, err := parseTime(windowEnd)
	if err != nil {
		return Record{}, err
	}
	ca, err := parseTime(createdAt)
	if err != nil {
		return Record{}, err
	}
	body, err := json.Marshal(struct {
		ID              string          `json:"id"`
		ObserverName    string          `json:"observer_name"`
		Pattern         string          `json:"pattern"`
		Severity        string          `json:"severity"`
		Summary         string          `json:"summary"`
		WindowStart     time.Time       `json:"window_start"`
		WindowEnd       time.Time       `json:"window_end"`
		SourceRecordIDs json.RawMessage `json:"source_record_ids"`
		CreatedAt       time.Time       `json:"created_at"`
	}{id, observerName, pattern, severity, summary, ws, we, rawOrNull(sourceRecordIDs), ca})
	if err != nil {
		return Record{}, fmt.Errorf("srs: marshal insight: %w", err)
	}
	return Record{ID: id, TimeUnixNano: unixNano(ca), Body: body, SchemaVersion: schemaVersion}, nil
}

func scanActionLog(rows *sql.Rows) (Record, error) {
	var id, executionID, stepID, description, state, timestamp, schemaVersion string
	if err := rows.Scan(&id, &executionID, &stepID, &description, &state, &timestamp, &schemaVersion); err != nil {
		return Record{}, fmt.Errorf("srs: scan action log: %w", err)
	}
	ts, err := parseTime(timestamp)
	if err != nil {
		return Record{}, err
	}
	body, err := json.Marshal(struct {
		ID          string    `json:"id"`
		ExecutionID string    `json:"execution_id"`
		StepID      string    `json:"step_id,omitempty"`
		Description string    `json:"description"`
		State       string    `json:"state"`
		Timestamp   time.Time `json:"timestamp"`
	}{id, executionID, stepID, description, state, ts})
	if err != nil {
		return Record{}, fmt.Errorf("srs: marshal action log: %w", err)
	}
	return Record{ID: id, TimeUnixNano: unixNano(ts), Body: body, SchemaVersion: schemaVersion}, nil
}

func scanRecordedAction(rows *sql.Rows) (Record, error) {
	var id, executionID, insightID, operatorName, state, timestamp, schemaVersion string
	if err := rows.Scan(&id, &executionID, &insightID, &operatorName, &state, &timestamp, &schemaVersion); err != nil {
		return Record{}, fmt.Errorf("srs: scan recorded action: %w", err)
	}
	ts, err := parseTime(timestamp)
	if err != nil {
		return Record{}, err
	}
	body, err := json.Marshal(struct {
		ID           string    `json:"id"`
		ExecutionID  string    `json:"execution_id"`
		InsightID    string    `json:"insight_id"`
		OperatorName string    `json:"operator_name"`
		State        string    `json:"state"`
		Timestamp    time.Time `json:"timestamp"`
	}{id, executionID, insightID, operatorName, state, ts})
	if err != nil {
		return Record{}, fmt.Errorf("srs: marshal recorded action: %w", err)
	}
	return Record{ID: id, TimeUnixNano: unixNano(ts), Body: body, SchemaVersion: schemaVersion}, nil
}

// rawOrNull returns s as raw JSON, defaulting to an empty array when blank and
// falling back to a JSON string when s is not valid JSON. This keeps the
// marshaled body valid so a single malformed column cannot fail the whole page.
func rawOrNull(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("[]")
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	quoted, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage("[]")
	}
	return json.RawMessage(quoted)
}

// flattenAttrs parses a JSON object from Doris's VARIANT column and flattens
// nested keys with dot notation, matching the doris store's flattenAttrs helper.
func flattenAttrs(raw string) map[string]string {
	out := map[string]string{}
	var nested map[string]any
	if err := json.Unmarshal([]byte(raw), &nested); err != nil {
		return out
	}
	flattenInto("", nested, out)
	return out
}

func flattenInto(prefix string, m map[string]any, out map[string]string) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			flattenInto(key, val, out)
		default:
			out[key] = fmt.Sprintf("%v", val)
		}
	}
}
