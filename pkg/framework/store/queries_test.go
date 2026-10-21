package store_test

import (
	"testing"
	"time"

	"github.tools.sap/autonomous-operations/aos-framework/pkg/framework/store"
)

// ---------------------------------------------------------------------------
// TelemetryTimeRangeQuery
// ---------------------------------------------------------------------------

func TestTelemetryTimeRangeQuery_Validate(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Hour)

	tests := []struct {
		name    string
		query   store.TelemetryTimeRangeQuery
		wantErr bool
	}{
		{
			name:    "both zero",
			query:   store.TelemetryTimeRangeQuery{},
			wantErr: true,
		},
		{
			name:    "only Start set",
			query:   store.TelemetryTimeRangeQuery{Start: now},
			wantErr: false,
		},
		{
			name:    "only End set",
			query:   store.TelemetryTimeRangeQuery{End: now},
			wantErr: false,
		},
		{
			name:    "Start before End",
			query:   store.TelemetryTimeRangeQuery{Start: now, End: later},
			wantErr: false,
		},
		{
			name:    "Start equal to End",
			query:   store.TelemetryTimeRangeQuery{Start: now, End: now},
			wantErr: false,
		},
		{
			name:    "Start after End",
			query:   store.TelemetryTimeRangeQuery{Start: later, End: now},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.query.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TelemetryCursorQuery
// ---------------------------------------------------------------------------

func TestTelemetryCursorQuery_Validate(t *testing.T) {
	tests := []struct {
		name    string
		query   store.TelemetryCursorQuery
		wantErr bool
	}{
		{
			name:    "count zero",
			query:   store.TelemetryCursorQuery{Count: 0},
			wantErr: true,
		},
		{
			name:    "count negative",
			query:   store.TelemetryCursorQuery{Count: -1},
			wantErr: true,
		},
		{
			name:    "count positive",
			query:   store.TelemetryCursorQuery{Count: 10},
			wantErr: false,
		},
		{
			name:    "count one",
			query:   store.TelemetryCursorQuery{Count: 1},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.query.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
