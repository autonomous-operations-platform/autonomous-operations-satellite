package doris

import (
	"fmt"
	"time"
)

// dorisTime scans a Doris DATETIME column into a time.Time.
// go-mysql returns DATETIME values as text ("2006-01-02 15:04:05[.ffffff]");
// scanning into a plain time.Time fails (ADR AO-006, condition 2).
type dorisTime struct{ t time.Time }

func (d *dorisTime) Scan(src any) error {
	switch v := src.(type) {
	case []byte:
		return d.parse(string(v))
	case string:
		return d.parse(v)
	case time.Time:
		d.t = v.UTC()
		return nil
	case nil:
		d.t = time.Time{}
		return nil
	default:
		return fmt.Errorf("doris: cannot scan %T into DateTime", src)
	}
}

var dtLayouts = []string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

func (d *dorisTime) parse(s string) error {
	for _, layout := range dtLayouts {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			d.t = t
			return nil
		}
	}
	return fmt.Errorf("doris: parse datetime %q: unrecognised format", s)
}

// timeArg formats t as a UTC DATETIME(6) string for use as a Doris query
// argument. go-mysql does not accept time.Time query arguments directly
// (ADR AO-006, condition 1).
func timeArg(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.000000")
}
