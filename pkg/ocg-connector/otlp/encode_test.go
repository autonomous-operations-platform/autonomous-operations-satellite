package otlp

import (
	"testing"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/schema"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/srs"
)

func TestEncodeMatchesADRShape(t *testing.T) {
	rec := srs.Record{
		ID:            "01934ab2-1234-7000-8000-000000000001",
		ScopeName:     "satellite/srs/telemetry",
		TimeUnixNano:  1762000100000000000,
		Body:          []byte(`{"collector":"cpu","value":0.43}`),
		SchemaVersion: "1.0.0",
	}

	ld := Encode([]srs.Record{rec})

	rls := ld.GetResourceLogs()
	if len(rls) != 1 {
		t.Fatalf("resource_logs = %d, want 1", len(rls))
	}
	sls := rls[0].GetScopeLogs()
	if len(sls) != 1 {
		t.Fatalf("scope_logs = %d, want 1", len(sls))
	}
	scope := sls[0].GetScope()
	if scope.GetName() != rec.ScopeName {
		t.Errorf("scope name = %q, want %q", scope.GetName(), rec.ScopeName)
	}
	if scope.GetVersion() != schema.SourceSchemaVersion {
		t.Errorf("scope version = %q, want %q", scope.GetVersion(), schema.SourceSchemaVersion)
	}

	lrs := sls[0].GetLogRecords()
	if len(lrs) != 1 {
		t.Fatalf("log_records = %d, want 1", len(lrs))
	}
	lr := lrs[0]
	if lr.GetTimeUnixNano() != rec.TimeUnixNano {
		t.Errorf("time_unix_nano = %d, want %d", lr.GetTimeUnixNano(), rec.TimeUnixNano)
	}
	if got := lr.GetBody().GetStringValue(); got != string(rec.Body) {
		t.Errorf("body = %q, want %q", got, rec.Body)
	}
	attrs := lr.GetAttributes()
	if len(attrs) != 2 {
		t.Fatalf("attributes = %d, want 2 (record_id + schema_version)", len(attrs))
	}
	if attrs[0].GetKey() != recordIDAttr || attrs[0].GetValue().GetStringValue() != rec.ID {
		t.Errorf("attrs[0] = %q=%q, want %q=%q", attrs[0].GetKey(), attrs[0].GetValue().GetStringValue(), recordIDAttr, rec.ID)
	}
	if attrs[1].GetKey() != schemaVersionAttr || attrs[1].GetValue().GetStringValue() != rec.SchemaVersion {
		t.Errorf("attrs[1] = %q=%q, want %q=%q", attrs[1].GetKey(), attrs[1].GetValue().GetStringValue(), schemaVersionAttr, rec.SchemaVersion)
	}
}

// TestEncodeGroupsByScope confirms records of one type share a single ScopeLogs
// with their LogRecords in order, while a distinct type gets its own ScopeLogs.
func TestEncodeGroupsByScope(t *testing.T) {
	recs := []srs.Record{
		{ID: "01934ab2-1234-7000-8000-000000000001", ScopeName: "satellite/logs/doris"},
		{ID: "01934ab2-1234-7000-8000-000000000002", ScopeName: "satellite/logs/satellite"},
		{ID: "01934ab2-1234-7000-8000-000000000003", ScopeName: "satellite/logs/doris"},
	}

	ld := Encode(recs)

	rls := ld.GetResourceLogs()
	if len(rls) != 1 {
		t.Fatalf("resource_logs = %d, want 1", len(rls))
	}
	sls := rls[0].GetScopeLogs()
	if len(sls) != 2 {
		t.Fatalf("scope_logs = %d, want 2", len(sls))
	}
	// First-seen scope order is preserved: doris, then satellite.
	if sls[0].GetScope().GetName() != "satellite/logs/doris" {
		t.Errorf("scope[0] = %q", sls[0].GetScope().GetName())
	}
	if sls[1].GetScope().GetName() != "satellite/logs/satellite" {
		t.Errorf("scope[1] = %q", sls[1].GetScope().GetName())
	}
	// The doris scope holds both doris records, in order.
	doris := sls[0].GetLogRecords()
	if len(doris) != 2 {
		t.Fatalf("doris log_records = %d, want 2", len(doris))
	}
	if id := doris[0].GetAttributes()[0].GetValue().GetStringValue(); id != recs[0].ID {
		t.Errorf("doris[0] id = %q, want %q", id, recs[0].ID)
	}
	if id := doris[1].GetAttributes()[0].GetValue().GetStringValue(); id != recs[2].ID {
		t.Errorf("doris[1] id = %q, want %q", id, recs[2].ID)
	}
}

func TestEncodeEmpty(t *testing.T) {
	if ld := Encode(nil); len(ld.GetResourceLogs()) != 0 {
		t.Errorf("resource_logs = %d, want 0", len(ld.GetResourceLogs()))
	}
}
