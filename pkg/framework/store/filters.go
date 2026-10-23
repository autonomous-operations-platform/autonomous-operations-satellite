package store

// TelemetryFilter selects TelemetryRecords by signal type and attribute pairs.
// Zero value for Signal and empty Attributes match all records.
type TelemetryFilter struct {
	Signal     Signal
	Attributes map[string]string
}

func (f TelemetryFilter) Matches(r TelemetryRecord) bool {
	if f.Signal != "" && r.Signal != f.Signal {
		return false
	}
	for k, v := range f.Attributes {
		if r.Attributes[k] != v {
			return false
		}
	}
	return true
}

// InsightFilter selects Insights by pattern name and severity.
// Empty slices match all values.
type InsightFilter struct {
	Patterns   []string
	Severities []Severity
}

func (f InsightFilter) Matches(insight Insight) bool {
	if len(f.Patterns) > 0 {
		ok := false
		for _, p := range f.Patterns {
			if p == insight.Pattern {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(f.Severities) > 0 {
		ok := false
		for _, s := range f.Severities {
			if s == insight.Severity {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
