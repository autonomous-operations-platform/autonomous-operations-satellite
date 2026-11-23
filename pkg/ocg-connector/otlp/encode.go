// Package otlp encodes satellite records as OpenTelemetry LogsData, with
// InstrumentationScope.Name as the record-type discriminator.
package otlp

import (
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/schema"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/srs"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

const (
	recordIDAttr      = "record_id"
	schemaVersionAttr = "schema_version"
)

// Encode groups records into one ScopeLogs per record type, in first-seen
// order. An empty page yields a LogsData with no ResourceLogs.
func Encode(records []srs.Record) *logspb.LogsData {
	if len(records) == 0 {
		return &logspb.LogsData{}
	}

	var scopeLogs []*logspb.ScopeLogs
	byScope := make(map[string]*logspb.ScopeLogs)
	for _, rec := range records {
		sl, ok := byScope[rec.ScopeName]
		if !ok {
			sl = &logspb.ScopeLogs{
				Scope: &commonpb.InstrumentationScope{
					Name:    rec.ScopeName,
					Version: schema.SourceSchemaVersion,
				},
			}
			byScope[rec.ScopeName] = sl
			scopeLogs = append(scopeLogs, sl)
		}
		sl.LogRecords = append(sl.LogRecords, logRecord(rec))
	}

	return &logspb.LogsData{
		ResourceLogs: []*logspb.ResourceLogs{{ScopeLogs: scopeLogs}},
	}
}

func logRecord(rec srs.Record) *logspb.LogRecord {
	return &logspb.LogRecord{
		TimeUnixNano: rec.TimeUnixNano,
		Body: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_StringValue{StringValue: string(rec.Body)},
		},
		Attributes: []*commonpb.KeyValue{
			{Key: recordIDAttr, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: rec.ID}}},
			{Key: schemaVersionAttr, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: rec.SchemaVersion}}},
		},
	}
}
