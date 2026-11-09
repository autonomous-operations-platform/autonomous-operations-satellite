package otlp

import (
	"os"
	"testing"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

func TestDecodeLogs_Fixture(t *testing.T) {
	body, err := os.ReadFile("testdata/logs.json")
	if err != nil {
		t.Fatal(err)
	}
	records, err := decode(store.SignalLogs, body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("want 3 records, got %d", len(records))
	}
	for i, r := range records {
		if r.Signal != store.SignalLogs {
			t.Errorf("records[%d].Signal = %q", i, r.Signal)
		}
		if r.Attributes == nil {
			t.Errorf("records[%d].Attributes is nil", i)
		}
	}

	first := records[0].Attributes
	want := map[string]string{
		"service.name":            "record-wins", // record attribute beats resource attribute
		"host.cpu.count":          "8",           // quoted intValue
		"retries":                 "3",           // numeric intValue
		"ratio":                   "0.5",
		"ok":                      "false",
		"time_unix_nano":          "1790755918424000000",
		"observed_time_unix_nano": "1790755918424575488",
		"fqdn":                    "mock-host-01.example.com",
		"severity_code":           "6",
		"sampled":                 "true",
		"source":                  "from-body", // JSON body field beats record attribute
	}
	for k, v := range want {
		if got := first[k]; got != v {
			t.Errorf("attrs[%q] = %q, want %q", k, got, v)
		}
	}
	for _, k := range []string{"tags", "meta"} {
		if _, ok := first[k]; ok {
			t.Errorf("non-scalar %q must be skipped", k)
		}
	}
	if string(records[0].Payload) == "" {
		t.Error("records[0].Payload is empty")
	}

	if got := string(records[1].Payload); got != "42" {
		t.Errorf("scalar body payload = %q, want 42", got)
	}
	if got := records[1].Attributes["time_unix_nano"]; got != "1790755918425000000" {
		t.Errorf("numeric timeUnixNano = %q", got)
	}
	if got := string(records[2].Payload); got != "plain text line" {
		t.Errorf("plain body payload = %q", got)
	}
	if got := records[2].Attributes["service.name"]; got != "mock-service" {
		t.Errorf("resource attribute on second record = %q, want mock-service", got)
	}
}

func TestDecodeLogs_ResourceAttrsNotShared(t *testing.T) {
	body := []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"r","value":{"stringValue":"1"}}]},
"scopeLogs":[{"logRecords":[{"attributes":[{"key":"x","value":{"stringValue":"a"}}]},{}]}]}]}`)
	records, err := decode(store.SignalLogs, body)
	if err != nil || len(records) != 2 {
		t.Fatalf("decode: %v, %d records", err, len(records))
	}
	if _, ok := records[1].Attributes["x"]; ok {
		t.Error("record attribute leaked into a sibling record")
	}
	if records[1].Payload != nil {
		t.Errorf("empty body payload = %q, want nil", records[1].Payload)
	}
}

func TestDecode_EmptyLogsRequest(t *testing.T) {
	for _, body := range []string{`{}`, `{"resourceLogs":[]}`, `{"resourceLogs":[{"scopeLogs":[{"logRecords":[]}]}]}`} {
		records, err := decode(store.SignalLogs, []byte(body))
		if err != nil || len(records) != 0 {
			t.Errorf("%s: want 0 records and nil error, got %d, %v", body, len(records), err)
		}
	}
}

func TestDecode_InvalidJSON(t *testing.T) {
	for _, sig := range []store.Signal{store.SignalLogs, store.SignalMetrics, store.SignalTraces} {
		for _, body := range []string{`{not json`, `[]`, `"text"`, `{"resourceLogs":{}, "resourceMetrics":{}, "resourceSpans":{}}`} {
			if _, err := decode(sig, []byte(body)); err == nil {
				t.Errorf("%s %s: want error", sig, body)
			}
		}
	}
}

func TestDecode_InvalidIntValue(t *testing.T) {
	body := []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"k","value":{"intValue":"abc"}}]}}]}`)
	if _, err := decode(store.SignalLogs, body); err == nil {
		t.Error("want error for non-numeric intValue")
	}
}

func TestDecode_Metrics(t *testing.T) {
	body := []byte(`{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc"}}]},
"scopeMetrics":[{"metrics":[
 {"gauge":{"dataPoints":[{"attributes":[{"key":"g","value":{"stringValue":"1"}}]}]}},
 {"sum":{"dataPoints":[{"attributes":[{"key":"s","value":{"stringValue":"1"}}]}]}},
 {"histogram":{"dataPoints":[{"attributes":[{"key":"h","value":{"stringValue":"1"}}]}]}},
 {"exponentialHistogram":{"dataPoints":[{"attributes":[{"key":"e","value":{"stringValue":"1"}}]}]}},
 {"summary":{"dataPoints":[{"attributes":[{"key":"m","value":{"stringValue":"1"}}]}]}}
]}]}]}`)
	records, err := decode(store.SignalMetrics, body)
	if err != nil || len(records) != 1 {
		t.Fatalf("decode: %v, %d records", err, len(records))
	}
	r := records[0]
	if r.Signal != store.SignalMetrics || string(r.Payload) != string(body) {
		t.Errorf("want metrics record with raw body payload, got %q", r.Signal)
	}
	for _, k := range []string{"service.name", "g", "s", "h", "e", "m"} {
		if r.Attributes[k] == "" {
			t.Errorf("missing attribute %q", k)
		}
	}
}

func TestDecode_TracesWithoutAttributes(t *testing.T) {
	body := []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"op"}]}]}]}`)
	records, err := decode(store.SignalTraces, body)
	if err != nil || len(records) != 1 {
		t.Fatalf("decode: %v, %d records", err, len(records))
	}
	if records[0].Attributes == nil || len(records[0].Attributes) != 0 {
		t.Errorf("want empty non-nil attributes, got %#v", records[0].Attributes)
	}
}

func TestDecode_TraceAttributes(t *testing.T) {
	body := []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"r","value":{"stringValue":"1"}}]},
"scopeSpans":[{"spans":[{"attributes":[{"key":"http.status","value":{"intValue":200}}]}]}]}]}`)
	records, err := decode(store.SignalTraces, body)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Attributes["r"] != "1" || records[0].Attributes["http.status"] != "200" {
		t.Errorf("attributes = %#v", records[0].Attributes)
	}
}
