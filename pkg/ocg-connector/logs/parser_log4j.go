package logs

import (
	"strings"
	"time"
)

// Log4jParser reads the leading timestamp from a Log4j / Doris FE log line,
// with or without milliseconds, falling back to now.
func Log4jParser(line []byte) time.Time {
	if len(line) >= 23 {
		s := strings.ReplaceAll(string(line[:23]), ",", ".")
		if t, err := time.ParseInLocation("2006-01-02 15:04:05.000", s, time.UTC); err == nil {
			return t
		}
	}
	if len(line) >= 19 {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", string(line[:19]), time.UTC); err == nil {
			return t
		}
	}
	return time.Now()
}
