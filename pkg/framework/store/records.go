package store

import "time"

// Signal identifies the type of telemetry data.
type Signal string

const (
	SignalMetrics Signal = "metrics"
	SignalLogs    Signal = "logs"
	SignalTraces  Signal = "traces"
)

// Severity classifies the urgency of an Insight.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type TelemetryRecord struct {
	ID         RecordID          `json:"id"`
	Signal     Signal            `json:"signal"`
	ReceivedAt time.Time         `json:"received_at"`
	Payload    []byte            `json:"payload"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type NewTelemetryRecord struct {
	Signal     Signal
	Payload    []byte
	Attributes map[string]string
}

type Insight struct {
	ID              RecordID   `json:"id"`
	ObserverName    string     `json:"observer_name"`
	Pattern         string     `json:"pattern"`
	Severity        Severity   `json:"severity"`
	Summary         string     `json:"summary"`
	WindowStart     time.Time  `json:"window_start"`
	WindowEnd       time.Time  `json:"window_end"`
	SourceRecordIDs []RecordID `json:"source_record_ids,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type NewInsight struct {
	Pattern         string
	Severity        Severity
	Summary         string
	WindowStart     time.Time
	WindowEnd       time.Time
	SourceRecordIDs []RecordID
}

// ActionLogState describes the lifecycle state of one operator step.
type ActionLogState string

const (
	// ActionLogStateAttempting marks the start of a step. A paired Success or
	// Failed entry with the same StepID is always expected. An unpaired
	// Attempting entry indicates the operator crashed mid-step.
	ActionLogStateAttempting ActionLogState = "attempting"
	ActionLogStateSuccess    ActionLogState = "success"
	ActionLogStateFailed     ActionLogState = "failed"
	// ActionLogStateInfo is self-contained — no paired entry is expected.
	ActionLogStateInfo ActionLogState = "info"
)

type ActionLog struct {
	ID          RecordID       `json:"id"`
	ExecutionID RecordID       `json:"execution_id"`
	StepID      string         `json:"step_id,omitempty"`
	Description string         `json:"description"`
	State       ActionLogState `json:"state"`
	Timestamp   time.Time      `json:"timestamp"`
}

type NewActionLog struct {
	StepID      string
	Description string
	State       ActionLogState
}

type RecordedActionState string

const (
	StateStarted RecordedActionState = "started"
	StateSuccess RecordedActionState = "success"
	StateFailed  RecordedActionState = "failed"
)

type RecordedAction struct {
	ID           RecordID            `json:"id"`
	ExecutionID  RecordID            `json:"execution_id"`
	InsightID    RecordID            `json:"insight_id"`
	OperatorName string              `json:"operator_name"`
	State        RecordedActionState `json:"state"`
	Timestamp    time.Time           `json:"timestamp"`
}
