package otlp

import (
	"encoding/json"
	"maps"
	"strconv"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

// decode turns an OTLP JSON body into telemetry records.
//
// Logs expand to one record per log record. Metrics and traces become one
// record per request with the raw body as payload. Every record has a non-nil
// attribute map. Any JSON error is returned unchanged.
func decode(signal store.Signal, body []byte) ([]store.NewTelemetryRecord, error) {
	var attrs map[string]string
	var err error
	switch signal {
	case store.SignalLogs:
		return decodeLogs(body)
	case store.SignalMetrics:
		attrs, err = metricAttrs(body)
	default: // store.SignalTraces; config validation rejects other signals.
		attrs, err = traceAttrs(body)
	}
	if err != nil {
		return nil, err
	}
	return []store.NewTelemetryRecord{{Signal: signal, Payload: body, Attributes: attrs}}, nil
}

// anyValue is the scalar part of an OTLP AnyValue. Arrays and key-value lists
// are skipped. protojson encodes int64 as a quoted string, but other encoders
// send a number; json.Number accepts both.
type anyValue struct {
	StringValue *string      `json:"stringValue"`
	IntValue    *json.Number `json:"intValue"`
	DoubleValue *float64     `json:"doubleValue"`
	BoolValue   *bool        `json:"boolValue"`
}

func (v anyValue) scalar() (string, bool) {
	switch {
	case v.StringValue != nil:
		return *v.StringValue, true
	case v.IntValue != nil:
		return v.IntValue.String(), true
	case v.DoubleValue != nil:
		return strconv.FormatFloat(*v.DoubleValue, 'g', -1, 64), true
	case v.BoolValue != nil:
		return strconv.FormatBool(*v.BoolValue), true
	}
	return "", false
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type resource struct {
	Attributes []keyValue `json:"attributes"`
}

type dataPoints struct {
	DataPoints []struct {
		Attributes []keyValue `json:"attributes"`
	} `json:"dataPoints"`
}

type metric struct {
	Gauge                *dataPoints `json:"gauge"`
	Sum                  *dataPoints `json:"sum"`
	Histogram            *dataPoints `json:"histogram"`
	ExponentialHistogram *dataPoints `json:"exponentialHistogram"`
	Summary              *dataPoints `json:"summary"`
}

type logRecord struct {
	TimeUnixNano         json.Number `json:"timeUnixNano"`
	ObservedTimeUnixNano json.Number `json:"observedTimeUnixNano"`
	Body                 anyValue    `json:"body"`
	Attributes           []keyValue  `json:"attributes"`
}

func mergeAttrs(dst map[string]string, kvs []keyValue) {
	for _, kv := range kvs {
		if s, ok := kv.Value.scalar(); ok {
			dst[kv.Key] = s
		}
	}
}

func metricAttrs(body []byte) (map[string]string, error) {
	var data struct {
		ResourceMetrics []struct {
			Resource     resource `json:"resource"`
			ScopeMetrics []struct {
				Metrics []metric `json:"metrics"`
			} `json:"scopeMetrics"`
		} `json:"resourceMetrics"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	attrs := make(map[string]string)
	for _, rm := range data.ResourceMetrics {
		mergeAttrs(attrs, rm.Resource.Attributes)
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				for _, dps := range []*dataPoints{m.Gauge, m.Sum, m.Histogram, m.ExponentialHistogram, m.Summary} {
					if dps == nil {
						continue
					}
					for _, dp := range dps.DataPoints {
						mergeAttrs(attrs, dp.Attributes)
					}
				}
			}
		}
	}
	return attrs, nil
}

func traceAttrs(body []byte) (map[string]string, error) {
	var data struct {
		ResourceSpans []struct {
			Resource   resource `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					Attributes []keyValue `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	attrs := make(map[string]string)
	for _, rs := range data.ResourceSpans {
		mergeAttrs(attrs, rs.Resource.Attributes)
		for _, ss := range rs.ScopeSpans {
			for _, span := range ss.Spans {
				mergeAttrs(attrs, span.Attributes)
			}
		}
	}
	return attrs, nil
}

// decodeLogs returns one record per log record. Attributes merge in this
// order, later keys winning: resource attributes, log record attributes,
// timestamps, then top-level scalar fields of a JSON object body.
func decodeLogs(body []byte) ([]store.NewTelemetryRecord, error) {
	var data struct {
		ResourceLogs []struct {
			Resource  resource `json:"resource"`
			ScopeLogs []struct {
				LogRecords []logRecord `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}

	var records []store.NewTelemetryRecord
	for _, rl := range data.ResourceLogs {
		resAttrs := make(map[string]string)
		mergeAttrs(resAttrs, rl.Resource.Attributes)
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				attrs := make(map[string]string, len(resAttrs)+len(lr.Attributes))
				maps.Copy(attrs, resAttrs)
				mergeAttrs(attrs, lr.Attributes)
				if lr.TimeUnixNano != "" {
					attrs["time_unix_nano"] = lr.TimeUnixNano.String()
				}
				if lr.ObservedTimeUnixNano != "" {
					attrs["observed_time_unix_nano"] = lr.ObservedTimeUnixNano.String()
				}
				var payload []byte
				if s, ok := lr.Body.scalar(); ok {
					payload = []byte(s)
				}
				if lr.Body.StringValue != nil {
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
	return records, nil
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
