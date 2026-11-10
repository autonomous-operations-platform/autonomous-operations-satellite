package otlp

import (
	"encoding/json"
	"iter"
	"maps"
	"strconv"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

// decode turns an OTLP JSON body into telemetry records.
//
// Logs expand to one record per log record. Metrics and traces become one
// record per request with the raw body as payload. Every record has a non-nil
// attribute map. Any JSON error is returned unchanged.
func decode(signal store.Signal, body []byte) ([]store.NewTelemetryRecord, error) {
	switch signal {
	case store.SignalLogs:
		ld, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(body)
		if err != nil {
			return nil, err
		}
		return logRecords(ld), nil
	case store.SignalMetrics:
		md, err := (&pmetric.JSONUnmarshaler{}).UnmarshalMetrics(body)
		if err != nil {
			return nil, err
		}
		return []store.NewTelemetryRecord{{Signal: signal, Payload: body, Attributes: metricAttrs(md)}}, nil
	default: // store.SignalTraces; config validation rejects other signals.
		td, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(body)
		if err != nil {
			return nil, err
		}
		attrs := make(map[string]string)
		for _, rs := range td.ResourceSpans().All() {
			mergeAttrs(attrs, rs.Resource().Attributes())
			for _, ss := range rs.ScopeSpans().All() {
				for _, span := range ss.Spans().All() {
					mergeAttrs(attrs, span.Attributes())
				}
			}
		}
		return []store.NewTelemetryRecord{{Signal: signal, Payload: body, Attributes: attrs}}, nil
	}
}

// mergeAttrs copies the scalar attributes into dst, later keys winning.
// Maps, slices, bytes, and empty values are skipped.
func mergeAttrs(dst map[string]string, attrs pcommon.Map) {
	for k, v := range attrs.All() {
		if s, ok := scalar(v); ok {
			dst[k] = s
		}
	}
}

func scalar(v pcommon.Value) (string, bool) {
	switch v.Type() {
	case pcommon.ValueTypeStr, pcommon.ValueTypeInt, pcommon.ValueTypeDouble, pcommon.ValueTypeBool:
		return v.AsString(), true
	}
	return "", false
}

func metricAttrs(md pmetric.Metrics) map[string]string {
	attrs := make(map[string]string)
	for _, rm := range md.ResourceMetrics().All() {
		mergeAttrs(attrs, rm.Resource().Attributes())
		for _, sm := range rm.ScopeMetrics().All() {
			for _, m := range sm.Metrics().All() {
				switch m.Type() {
				case pmetric.MetricTypeGauge:
					mergeDataPoints(attrs, m.Gauge().DataPoints().All())
				case pmetric.MetricTypeSum:
					mergeDataPoints(attrs, m.Sum().DataPoints().All())
				case pmetric.MetricTypeHistogram:
					mergeDataPoints(attrs, m.Histogram().DataPoints().All())
				case pmetric.MetricTypeExponentialHistogram:
					mergeDataPoints(attrs, m.ExponentialHistogram().DataPoints().All())
				case pmetric.MetricTypeSummary:
					mergeDataPoints(attrs, m.Summary().DataPoints().All())
				}
			}
		}
	}
	return attrs
}

func mergeDataPoints[P interface{ Attributes() pcommon.Map }](dst map[string]string, points iter.Seq2[int, P]) {
	for _, dp := range points {
		mergeAttrs(dst, dp.Attributes())
	}
}

// logRecords returns one record per log record. Attributes merge in this
// order, later keys winning: resource attributes, log record attributes,
// timestamps, then top-level scalar fields of a JSON object body.
func logRecords(ld plog.Logs) []store.NewTelemetryRecord {
	var records []store.NewTelemetryRecord
	for _, rl := range ld.ResourceLogs().All() {
		resAttrs := make(map[string]string)
		mergeAttrs(resAttrs, rl.Resource().Attributes())
		for _, sl := range rl.ScopeLogs().All() {
			for _, lr := range sl.LogRecords().All() {
				attrs := maps.Clone(resAttrs)
				mergeAttrs(attrs, lr.Attributes())
				if ts := lr.Timestamp(); ts != 0 {
					attrs["time_unix_nano"] = strconv.FormatUint(uint64(ts), 10)
				}
				if ts := lr.ObservedTimestamp(); ts != 0 {
					attrs["observed_time_unix_nano"] = strconv.FormatUint(uint64(ts), 10)
				}
				var payload []byte
				if s, ok := scalar(lr.Body()); ok {
					payload = []byte(s)
				}
				if lr.Body().Type() == pcommon.ValueTypeStr {
					mergeJSONBody(attrs, payload)
				}
				records = append(records, store.NewTelemetryRecord{
					Signal:     store.SignalLogs,
					Payload:    payload,
					Attributes: attrs,
				})
			}
		}
	}
	return records
}

// mergeJSONBody merges the top-level scalar fields of a JSON object body into
// dst. Bodies that are not JSON objects, and nested values, are skipped.
func mergeJSONBody(dst map[string]string, body []byte) {
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return
	}
	for k, v := range obj {
		switch v := v.(type) {
		case string:
			dst[k] = v
		case float64:
			dst[k] = strconv.FormatFloat(v, 'g', -1, 64)
		case bool:
			dst[k] = strconv.FormatBool(v)
		}
	}
}
