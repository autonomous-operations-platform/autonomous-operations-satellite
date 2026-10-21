package store_test

import (
	"testing"
	"time"

	"github.com/autonomous-operations/aos-framework/pkg/framework/store"
)

func TestNewID_CallsGenerate(t *testing.T) {
	const fakeID = "018e4a5c-8b1d-7000-8000-000000000001"
	called := false

	restore := store.SetUUIDGenerate(func() string {
		called = true
		return fakeID
	})
	defer restore()

	id := store.NewID()

	if !called {
		t.Error("NewID did not call uuidv7.Generate")
	}
	if id.String() != fakeID {
		t.Errorf("NewID() = %q, want %q", id, fakeID)
	}
}

func TestRecordID_Time_CallsExtractTime(t *testing.T) {
	const raw = "018e4a5c-8b1d-7000-8000-000000000001"
	want := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var gotArg string
	called := false

	restore := store.SetUUIDExtractTime(func(s string) time.Time {
		called = true
		gotArg = s
		return want
	})
	defer restore()

	got := store.RecordID(raw).Time()

	if !called {
		t.Error("RecordID.Time did not call uuidv7.ExtractTime")
	}
	if gotArg != raw {
		t.Errorf("uuidv7.ExtractTime called with %q, want %q", gotArg, raw)
	}
	if !got.Equal(want) {
		t.Errorf("RecordID.Time() = %v, want %v", got, want)
	}
}

func TestRecordID_Time_Empty(t *testing.T) {
	var id store.RecordID
	if got := id.Time(); !got.IsZero() {
		t.Errorf("empty RecordID.Time() = %v, want zero", got)
	}
}
