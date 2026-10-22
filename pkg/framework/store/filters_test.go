package store_test

import (
	"testing"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

// ---------------------------------------------------------------------------
// TelemetryFilter
// ---------------------------------------------------------------------------

func TestTelemetryFilter_Matches(t *testing.T) {
	base := store.TelemetryRecord{
		Signal:     store.SignalMetrics,
		Attributes: map[string]string{"env": "prod", "host": "node-1"},
	}

	tests := []struct {
		name   string
		filter store.TelemetryFilter
		record store.TelemetryRecord
		want   bool
	}{
		{
			name:   "zero filter matches any record",
			filter: store.TelemetryFilter{},
			record: base,
			want:   true,
		},
		{
			name:   "signal matches",
			filter: store.TelemetryFilter{Signal: store.SignalMetrics},
			record: base,
			want:   true,
		},
		{
			name:   "signal mismatch",
			filter: store.TelemetryFilter{Signal: store.SignalLogs},
			record: base,
			want:   false,
		},
		{
			name:   "attribute matches",
			filter: store.TelemetryFilter{Attributes: map[string]string{"env": "prod"}},
			record: base,
			want:   true,
		},
		{
			name:   "attribute value mismatch",
			filter: store.TelemetryFilter{Attributes: map[string]string{"env": "staging"}},
			record: base,
			want:   false,
		},
		{
			name:   "attribute key absent",
			filter: store.TelemetryFilter{Attributes: map[string]string{"region": "eu"}},
			record: base,
			want:   false,
		},
		{
			name: "signal and attribute both match",
			filter: store.TelemetryFilter{
				Signal:     store.SignalMetrics,
				Attributes: map[string]string{"host": "node-1"},
			},
			record: base,
			want:   true,
		},
		{
			name: "signal matches but attribute fails",
			filter: store.TelemetryFilter{
				Signal:     store.SignalMetrics,
				Attributes: map[string]string{"env": "staging"},
			},
			record: base,
			want:   false,
		},
		{
			name:   "zero filter matches record with no attributes",
			filter: store.TelemetryFilter{},
			record: store.TelemetryRecord{Signal: store.SignalLogs},
			want:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.filter.Matches(tc.record); got != tc.want {
				t.Errorf("Matches() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// InsightFilter
// ---------------------------------------------------------------------------

func TestInsightFilter_Matches(t *testing.T) {
	base := store.Insight{
		Pattern:  "high-cpu",
		Severity: store.SeverityWarning,
	}

	tests := []struct {
		name    string
		filter  store.InsightFilter
		insight store.Insight
		want    bool
	}{
		{
			name:    "zero filter matches any insight",
			filter:  store.InsightFilter{},
			insight: base,
			want:    true,
		},
		{
			name:    "pattern matches",
			filter:  store.InsightFilter{Patterns: []string{"high-cpu"}},
			insight: base,
			want:    true,
		},
		{
			name:    "pattern mismatch",
			filter:  store.InsightFilter{Patterns: []string{"disk-full"}},
			insight: base,
			want:    false,
		},
		{
			name:    "one of multiple patterns matches",
			filter:  store.InsightFilter{Patterns: []string{"disk-full", "high-cpu", "oom"}},
			insight: base,
			want:    true,
		},
		{
			name:    "severity matches",
			filter:  store.InsightFilter{Severities: []store.Severity{store.SeverityWarning}},
			insight: base,
			want:    true,
		},
		{
			name:    "severity mismatch",
			filter:  store.InsightFilter{Severities: []store.Severity{store.SeverityCritical}},
			insight: base,
			want:    false,
		},
		{
			name: "pattern and severity both match",
			filter: store.InsightFilter{
				Patterns:   []string{"high-cpu"},
				Severities: []store.Severity{store.SeverityWarning},
			},
			insight: base,
			want:    true,
		},
		{
			name: "pattern matches but severity fails",
			filter: store.InsightFilter{
				Patterns:   []string{"high-cpu"},
				Severities: []store.Severity{store.SeverityCritical},
			},
			insight: base,
			want:    false,
		},
		{
			name: "severity matches but pattern fails",
			filter: store.InsightFilter{
				Patterns:   []string{"disk-full"},
				Severities: []store.Severity{store.SeverityWarning},
			},
			insight: base,
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.filter.Matches(tc.insight); got != tc.want {
				t.Errorf("Matches() = %v, want %v", got, tc.want)
			}
		})
	}
}
