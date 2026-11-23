package logs

import (
	"encoding/json"
	"time"
)

// SlogParser reads the timestamp from a JSON slog line, falling back to now.
func SlogParser(line []byte) time.Time {
	if len(line) == 0 || line[0] != '{' {
		return time.Now()
	}
	var v struct {
		Time time.Time `json:"time"`
	}
	if err := json.Unmarshal(line, &v); err == nil && !v.Time.IsZero() {
		return v.Time
	}
	return time.Now()
}
