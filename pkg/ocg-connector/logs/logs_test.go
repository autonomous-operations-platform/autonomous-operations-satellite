package logs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- helpers ------------------------------------------------------------------

func newStore(t *testing.T, sources ...Source) *Store {
	t.Helper()
	return New(sources, nil)
}

// slogSrc creates a Source that uses SlogParser for timestamp extraction.
func slogSrc(component, path string) Source {
	return Source{Component: component, Path: path, ParseTime: SlogParser}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// tsNano formats t as a nanosecond Unix timestamp string for use as sinceID.
func tsNano(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}

// jsonLine produces a slog-JSON line with the given RFC3339Nano timestamp.
func jsonLine(ts time.Time, msg string) string {
	return fmt.Sprintf(`{"time":%q,"level":"INFO","msg":%q}`, ts.Format(time.RFC3339Nano), msg)
}

// --- SlogParser ---------------------------------------------------------------

func TestSlogParserJSON(t *testing.T) {
	ts := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	got := SlogParser([]byte(jsonLine(ts, "hello")))
	if !got.Equal(ts) {
		t.Errorf("got %v, want %v", got, ts)
	}
}

func TestSlogParserNotJSON(t *testing.T) {
	before := time.Now()
	got := SlogParser([]byte("plain text line"))
	if got.Before(before) {
		t.Errorf("fallback time %v before test start %v", got, before)
	}
}

func TestSlogParserInvalidJSON(t *testing.T) {
	before := time.Now()
	got := SlogParser([]byte("{not valid json}"))
	if got.Before(before) {
		t.Errorf("fallback time %v before test start %v", got, before)
	}
}

func TestSlogParserEmptyLine(t *testing.T) {
	before := time.Now()
	got := SlogParser(nil)
	if got.Before(before) {
		t.Errorf("fallback time %v before test start %v", got, before)
	}
}

// --- Log4jParser --------------------------------------------------------------

func TestLog4jParserWithMs(t *testing.T) {
	line := []byte("2024-11-01 10:00:00,123 INFO (fe-thread|42) [Class.run():9] started")
	got := Log4jParser(line)
	want := time.Date(2024, 11, 1, 10, 0, 0, 123_000_000, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLog4jParserWithoutMs(t *testing.T) {
	line := []byte("2024-11-01 10:00:01 INFO no-ms line")
	got := Log4jParser(line)
	want := time.Date(2024, 11, 1, 10, 0, 1, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLog4jParserNoMatch(t *testing.T) {
	before := time.Now()
	got := Log4jParser([]byte("short"))
	if got.Before(before) {
		t.Errorf("fallback time %v before test start %v", got, before)
	}
}

// --- parseSinceID -------------------------------------------------------------

func TestParseSinceID(t *testing.T) {
	ref := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		input   string
		wantErr bool
		check   func(int64) bool
	}{
		{"", false, func(n int64) bool { return n == 0 }},
		{strconv.FormatInt(ref.UnixNano(), 10), false, func(n int64) bool { return n == ref.UnixNano() }},
		{ref.Format(time.RFC3339Nano), false, func(n int64) bool { return n == ref.UnixNano() }},
		{ref.Format(time.RFC3339), false, func(n int64) bool { return n == ref.UnixNano() }},
		{"2024-11-01 10:00:00", false, func(n int64) bool { return n == ref.UnixNano() }},
		{"2024-11-01", false, func(n int64) bool {
			return n == time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC).UnixNano()
		}},
		{"01.11.2024", false, func(n int64) bool {
			return n == time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC).UnixNano()
		}},
		{"not-a-time", true, nil},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseSinceID(tc.input)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidCursor) {
					t.Fatalf("err = %v, want ErrInvalidCursor", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.check(got) {
				t.Errorf("parseSinceID(%q) = %d, unexpected", tc.input, got)
			}
		})
	}
}

// --- Query: basic -------------------------------------------------------------

func TestQueryJSONFormat(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.log")
	ts := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	line := jsonLine(ts, "hello")
	writeFile(t, src, line+"\n")

	s := newStore(t, slogSrc("app", src))
	recs, err := s.Query(context.Background(), "srs/logs/app", "", 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if string(recs[0].Body) != line {
		t.Errorf("body = %q, want %q", recs[0].Body, line)
	}
	if recs[0].ScopeName != "satellite/logs/app" {
		t.Errorf("scope = %q", recs[0].ScopeName)
	}
	if recs[0].TimeUnixNano != uint64(ts.UnixNano()) {
		t.Errorf("TimeUnixNano = %d, want %d", recs[0].TimeUnixNano, ts.UnixNano())
	}
	if recs[0].ID != tsNano(ts) {
		t.Errorf("ID = %q, want %q", recs[0].ID, tsNano(ts))
	}
}

func TestQueryLog4jFormat(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "doris.log")
	writeFile(t, src, "2024-11-01 10:00:00,123 INFO (fe|42) [Class.run():9] started\n")

	s := newStore(t, Source{Component: "doris", Path: src, ParseTime: Log4jParser})
	recs, err := s.Query(context.Background(), "srs/logs/doris", "", 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	want := time.Date(2024, 11, 1, 10, 0, 0, 123_000_000, time.UTC).UnixNano()
	if recs[0].TimeUnixNano != uint64(want) {
		t.Errorf("TimeUnixNano = %d, want %d", recs[0].TimeUnixNano, want)
	}
}

func TestQueryCRLFTrimmed(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.log")
	ts := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	writeFile(t, src, jsonLine(ts, "windows")+"\r\n")

	s := newStore(t, slogSrc("app", src))
	recs, err := s.Query(context.Background(), "srs/logs/app", "", 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if strings.HasSuffix(string(recs[0].Body), "\r") {
		t.Errorf("body has trailing CR: %q", recs[0].Body)
	}
}

func TestQueryEmptyLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.log")
	ts := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	writeFile(t, src, "\n\n"+jsonLine(ts, "real")+"\n\n")

	s := newStore(t, slogSrc("app", src))
	recs, err := s.Query(context.Background(), "srs/logs/app", "", 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1 (empty lines skipped)", len(recs))
	}
}

// --- Query: cursor ------------------------------------------------------------

func TestQueryCursorFilters(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.log")
	t1 := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 11, 1, 10, 0, 1, 0, time.UTC)
	line1 := jsonLine(t1, "first")
	line2 := jsonLine(t2, "second")
	writeFile(t, src, line1+"\n"+line2+"\n")

	s := newStore(t, slogSrc("app", src))
	recs, err := s.Query(context.Background(), "srs/logs/app", tsNano(t1), 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 1 || string(recs[0].Body) != line2 {
		t.Fatalf("cursor result = %+v", recs)
	}
}

func TestQueryCursorHumanReadable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.log")
	t1 := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 11, 1, 10, 0, 1, 0, time.UTC)
	writeFile(t, src, jsonLine(t1, "old")+"\n"+jsonLine(t2, "new")+"\n")

	s := newStore(t, slogSrc("app", src))
	// since_id as RFC3339 string instead of nanoseconds
	recs, err := s.Query(context.Background(), "srs/logs/app", t1.Format(time.RFC3339Nano), 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
}

func TestQueryCursorGermanDate(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.log")
	t1 := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	writeFile(t, src, jsonLine(t1, "nov")+"\n")

	s := newStore(t, slogSrc("app", src))
	// "01.11.2024" = Nov 1, 2024 midnight UTC — so t1 (10:00 UTC) is after it
	recs, err := s.Query(context.Background(), "srs/logs/app", "01.11.2024", 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
}

func TestQueryEmptyCursorReturnsAll(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.log")
	t1 := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 11, 1, 10, 0, 1, 0, time.UTC)
	writeFile(t, src, jsonLine(t1, "a")+"\n"+jsonLine(t2, "b")+"\n")

	s := newStore(t, slogSrc("app", src))
	recs, err := s.Query(context.Background(), "srs/logs/app", "", 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
}

func TestQueryLimit(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.log")
	base := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	var lines string
	for i := range 5 {
		lines += jsonLine(base.Add(time.Duration(i)*time.Second), fmt.Sprintf("line%d", i)) + "\n"
	}
	writeFile(t, src, lines)

	s := newStore(t, slogSrc("app", src))
	recs, err := s.Query(context.Background(), "srs/logs/app", "", 2)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
}

// --- Query: binary search (large file) ----------------------------------------

func TestQueryBinarySearchLargeFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "big.log")

	base := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	const n = 200
	var sb strings.Builder
	for i := range n {
		sb.WriteString(jsonLine(base.Add(time.Duration(i)*time.Second), fmt.Sprintf("msg-%04d", i)))
		sb.WriteByte('\n')
	}
	writeFile(t, src, sb.String())

	pivot := base.Add(100 * time.Second)
	s := newStore(t, slogSrc("app", src))
	recs, err := s.Query(context.Background(), "srs/logs/app", tsNano(pivot), 1000)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	// Expect 99 records: lines 101–199 (timestamps pivot+1s .. pivot+99s).
	if len(recs) != 99 {
		t.Fatalf("got %d records, want 99", len(recs))
	}
	for _, rec := range recs {
		if rec.TimeUnixNano <= uint64(pivot.UnixNano()) {
			t.Errorf("record timestamp %d not after cursor %d", rec.TimeUnixNano, pivot.UnixNano())
		}
	}
}

// --- Query: sources and errors ------------------------------------------------

func TestQuerySourceMissingIsSkipped(t *testing.T) {
	s := newStore(t, slogSrc("app", filepath.Join(t.TempDir(), "missing.log")))
	recs, err := s.Query(context.Background(), "srs/logs/app", "", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("got %d records, want 0", len(recs))
	}
}

func TestQueryUnknownStore(t *testing.T) {
	s := newStore(t)
	_, err := s.Query(context.Background(), "srs/logs/unknown", "", 10)
	if !errors.Is(err, ErrUnknownStore) {
		t.Fatalf("err = %v, want ErrUnknownStore", err)
	}
}

func TestQueryInvalidCursor(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, slogSrc("app", filepath.Join(dir, "app.log")))
	_, err := s.Query(context.Background(), "srs/logs/app", "not-a-time", 10)
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("err = %v, want ErrInvalidCursor", err)
	}
}

func TestQueryContextCanceled(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.log")
	ts := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	writeFile(t, src, jsonLine(ts, "x")+"\n")

	s := newStore(t, slogSrc("app", src))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Query(ctx, "srs/logs/app", "", 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestStoreCloseIsNoop(t *testing.T) {
	s := newStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// --- Handles ------------------------------------------------------------------

func TestHandles(t *testing.T) {
	if !Handles("srs/logs/satellite") {
		t.Error("Handles(logs/satellite) should be true")
	}
	if !Handles("srs/logs/doris") {
		t.Error("Handles(logs/doris) should be true")
	}
	if Handles("telemetry") {
		t.Error("Handles(telemetry) should be false")
	}
	if Handles("logs") {
		t.Error("Handles(logs) without slash should be false")
	}
}

// --- scanSource: direct calls -------------------------------------------------

func TestScanSourceLimitZero(t *testing.T) {
	recs, err := scanSource(Source{Path: "irrelevant"}, 0, 0)
	if err != nil || recs != nil {
		t.Fatalf("limit=0: got recs=%v err=%v, want nil,nil", recs, err)
	}
}

func TestScanSourceNilParseTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeFile(t, path, "some plain log line\n")
	// nil ParseTime uses time.Now() fallback; sinceNano=0 means no filtering
	recs, err := scanSource(Source{Component: "x", Path: path, ParseTime: nil}, 0, 10)
	if err != nil {
		t.Fatalf("scanSource: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
}

func TestQueryScanErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses file permissions")
	}
	dir := t.TempDir()
	restricted := filepath.Join(dir, "restricted.log")
	ts := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	writeFile(t, restricted, jsonLine(ts, "secret")+"\n")
	if err := os.Chmod(restricted, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(restricted, 0o600) })

	s := newStore(t, Source{Component: "restricted", Path: restricted, ParseTime: SlogParser})
	_, err := s.Query(context.Background(), "srs/logs/restricted", "", 10)
	if err == nil {
		t.Fatal("expected error for unreadable file")
	}
}

func TestQueryPermissionErrorWarnsAndContinues(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses file permissions")
	}
	dir := t.TempDir()
	restricted := filepath.Join(dir, "restricted.log")
	ts := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	writeFile(t, restricted, jsonLine(ts, "secret")+"\n")
	if err := os.Chmod(restricted, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(restricted, 0o600) })

	// scanSource called directly; open error (non-ErrNotExist) should return error.
	recs, err := scanSource(Source{Component: "r", Path: restricted, ParseTime: SlogParser}, 0, 10)
	if err == nil {
		t.Fatalf("expected error for restricted file, got %v records", len(recs))
	}
}

// TestScanSourceBinarySearchNoNewlines creates a file larger than 4096 bytes
// that contains no newlines after the first line. The binary search's
// readLineAt cannot find a '\n' and returns size, exercising the
// "hi = mid; continue" branch and the pos-advance loop in readLineAt.
func TestScanSourceBinarySearchNoNewlines(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "big.log")
	ts := time.Date(2024, 11, 1, 10, 0, 0, 0, time.UTC)
	line := jsonLine(ts, "only")
	content := line + strings.Repeat("X", 5000-len(line))
	writeFile(t, src, content)

	sinceNano := ts.Add(-time.Second).UnixNano()
	recs, err := scanSource(
		Source{Component: "app", Path: src, ParseTime: SlogParser},
		sinceNano,
		10,
	)
	if err != nil {
		t.Fatalf("scanSource: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
}

// --- readLineAt ---------------------------------------------------------------

func TestReadLineAtOffset0(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.log")
	writeFile(t, path, "first line\nsecond line\n")
	f, err := os.Open(path) //nolint:gosec // G304: test opens a temp file it created
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	start, content, err := readLineAt(f, 0, 23)
	if err != nil {
		t.Fatalf("readLineAt: %v", err)
	}
	if start != 0 || string(content) != "first line" {
		t.Errorf("start=%d content=%q", start, content)
	}
}

func TestReadLineAtMidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.log")
	writeFile(t, path, "first line\nsecond line\n")
	f, err := os.Open(path) //nolint:gosec // G304: test opens a temp file it created
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	start, content, err := readLineAt(f, 5, 23)
	if err != nil {
		t.Fatalf("readLineAt: %v", err)
	}
	if start != 11 || string(content) != "second line" {
		t.Errorf("start=%d content=%q", start, content)
	}
}

func TestReadLineAtPastLastNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.log")
	writeFile(t, path, "only line\n")
	f, err := os.Open(path) //nolint:gosec // G304: test opens a temp file it created
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	start, content, err := readLineAt(f, 9, 10)
	if err != nil {
		t.Fatalf("readLineAt: %v", err)
	}
	if start != 10 || content != nil {
		t.Errorf("expected (size=10, nil), got (%d, %q)", start, content)
	}
}
