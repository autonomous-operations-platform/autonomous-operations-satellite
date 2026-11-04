package doris

import (
	"testing"
	"time"
)

func TestDorisTime_Scan_string(t *testing.T) {
	var d dorisTime
	if err := d.Scan("2024-03-15 10:30:00"); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 3, 15, 10, 30, 0, 0, time.UTC)
	if !d.t.Equal(want) {
		t.Errorf("got %v, want %v", d.t, want)
	}
}

func TestDorisTime_Scan_bytes(t *testing.T) {
	var d dorisTime
	if err := d.Scan([]byte("2024-03-15 10:30:00")); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 3, 15, 10, 30, 0, 0, time.UTC)
	if !d.t.Equal(want) {
		t.Errorf("got %v, want %v", d.t, want)
	}
}

func TestDorisTime_Scan_fractional(t *testing.T) {
	var d dorisTime
	if err := d.Scan("2024-03-15 10:30:00.123456"); err != nil {
		t.Fatal(err)
	}
	if d.t.Nanosecond() == 0 {
		t.Error("expected non-zero nanoseconds from fractional seconds")
	}
}

func TestDorisTime_Scan_passthroughTime(t *testing.T) {
	var d dorisTime
	in := time.Date(2024, 3, 15, 10, 30, 0, 0, time.UTC)
	if err := d.Scan(in); err != nil {
		t.Fatal(err)
	}
	if !d.t.Equal(in) {
		t.Errorf("got %v, want %v", d.t, in)
	}
}

func TestDorisTime_Scan_nil(t *testing.T) {
	var d dorisTime
	if err := d.Scan(nil); err != nil {
		t.Fatal(err)
	}
	if !d.t.IsZero() {
		t.Errorf("expected zero time for nil, got %v", d.t)
	}
}

func TestDorisTime_Scan_wrongType(t *testing.T) {
	var d dorisTime
	if err := d.Scan(42); err == nil {
		t.Error("expected error for int input")
	}
}

func TestDorisTime_Scan_badFormat(t *testing.T) {
	var d dorisTime
	if err := d.Scan("not-a-datetime"); err == nil {
		t.Error("expected error for unrecognised format")
	}
}

func TestTimeArg_utcFormat(t *testing.T) {
	in := time.Date(2024, 3, 15, 10, 30, 0, 0, time.UTC)
	got := timeArg(in)
	want := "2024-03-15 10:30:00.000000"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTimeArg_convertsToUTC(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*60*60)
	in := time.Date(2024, 3, 15, 12, 30, 0, 0, loc) // 10:30 UTC
	got := timeArg(in)
	want := "2024-03-15 10:30:00.000000"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
