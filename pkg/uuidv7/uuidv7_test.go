package uuidv7_test

import (
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/autonomous-operations/aos-framework/pkg/uuidv7"
)

var uuidv7Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestGenerate_Format(t *testing.T) {
	s := uuidv7.Generate()
	if !uuidv7Re.MatchString(s) {
		t.Fatalf("invalid UUIDv7 format: %q", s)
	}
}

func TestGenerate_Uniqueness(t *testing.T) {
	const n = 10_000
	seen := make(map[string]struct{}, n)
	for i := range n {
		s := uuidv7.Generate()
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate UUID at iteration %d: %s", i, s)
		}
		seen[s] = struct{}{}
	}
}

func TestGenerate_Monotonic(t *testing.T) {
	const n = 10_000
	prev := uuidv7.Generate()
	for i := 1; i < n; i++ {
		curr := uuidv7.Generate()
		if strings.Compare(curr, prev) <= 0 {
			t.Fatalf("ordering violated at %d: %s <= %s", i, curr, prev)
		}
		prev = curr
	}
}

func TestGenerate_Concurrent(t *testing.T) {
	const goroutines = 10
	const perGoroutine = 1_000

	results := make([][]string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func() {
			defer wg.Done()
			ids := make([]string, perGoroutine)
			for j := range perGoroutine {
				ids[j] = uuidv7.Generate()
			}
			results[i] = ids
		}()
	}
	wg.Wait()

	seen := make(map[string]struct{}, goroutines*perGoroutine)
	for _, ids := range results {
		for _, id := range ids {
			if _, dup := seen[id]; dup {
				t.Fatalf("duplicate UUID under concurrency: %s", id)
			}
			seen[id] = struct{}{}
		}
	}
}

func TestExtractTime_RoundTrip(t *testing.T) {
	before := time.Now().UTC().Truncate(time.Millisecond)
	s := uuidv7.Generate()
	after := time.Now().UTC().Truncate(time.Millisecond).Add(time.Millisecond)

	got := uuidv7.ExtractTime(s)
	if got.Before(before) || got.After(after) {
		t.Errorf("extracted time %v outside [%v, %v]", got, before, after)
	}
}

func TestExtractTime_InvalidInputs(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"too short", "abc"},
		{"too long", strings.Repeat("a", 37)},
		{"non-hex", "zzzzzzzz-zzzz-7zzz-zzzz-zzzzzzzzzzzz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := uuidv7.ExtractTime(tc.input)
			if !got.IsZero() {
				t.Errorf("want zero time for %q, got %v", tc.input, got)
			}
		})
	}
}

func TestExtractTime_WithoutHyphens(t *testing.T) {
	s := uuidv7.Generate()
	noHyphens := strings.ReplaceAll(s, "-", "")

	want := uuidv7.ExtractTime(s)
	got := uuidv7.ExtractTime(noHyphens)

	if got.IsZero() {
		t.Fatal("want non-zero time for hyphen-free UUID")
	}
	if !got.Equal(want) {
		t.Errorf("hyphen-free %v != hyphenated %v", got, want)
	}
}

func TestIsValid(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		if !uuidv7.IsValid(uuidv7.Generate()) {
			t.Error("IsValid returned false for a generated UUIDv7")
		}
	})
	t.Run("empty", func(t *testing.T) {
		if uuidv7.IsValid("") {
			t.Error("IsValid returned true for empty string")
		}
	})
	t.Run("wrong version", func(t *testing.T) {
		if uuidv7.IsValid("00000000-0000-4000-8000-000000000000") {
			t.Error("IsValid returned true for UUIDv4")
		}
	})
	t.Run("wrong variant", func(t *testing.T) {
		if uuidv7.IsValid("00000000-0000-7000-0000-000000000000") {
			t.Error("IsValid returned true for wrong variant bits")
		}
	})
}
