package server

import (
	"context"
	"errors"
	"net"
	"testing"

	ocgconnectorv1 "github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/v1"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/srs"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// --- fakes --------------------------------------------------------------------

type fakeReader struct {
	records      []srs.Record
	err          error
	gotStore     string
	gotSince     string
	gotSchemaVer string
	gotLimit     int
}

func (f *fakeReader) Fetch(_ context.Context, store, sinceID, schemaVersion string, limit int) ([]srs.Record, error) {
	f.gotStore, f.gotSince, f.gotSchemaVer, f.gotLimit = store, sinceID, schemaVersion, limit
	return f.records, f.err
}

type fakeLogs struct {
	records  []srs.Record
	err      error
	gotStore string
	gotLimit int
}

func (f *fakeLogs) Query(_ context.Context, store, _ string, limit int) ([]srs.Record, error) {
	f.gotStore, f.gotLimit = store, limit
	return f.records, f.err
}

// dialServer starts the Server over an in-memory connection and returns a client.
func dialServer(t *testing.T, srv *Server) ocgconnectorv1.OcgConnectorServiceClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer()
	ocgconnectorv1.RegisterOcgConnectorServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return ocgconnectorv1.NewOcgConnectorServiceClient(conn)
}

func codeOf(err error) codes.Code { return status.Code(err) }

// --- tests --------------------------------------------------------------------

// countRecords totals the LogRecords across every ScopeLogs in the response's
// single LogsData, matching the number of satellite records returned.
func countRecords(resp *ocgconnectorv1.FetchResponse) int {
	n := 0
	for _, rl := range resp.GetRecords().GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			n += len(sl.GetLogRecords())
		}
	}
	return n
}

func TestFetchSRSStore(t *testing.T) {
	reader := &fakeReader{records: []srs.Record{
		{ID: "01934ab2-0000-7000-8000-000000000001", ScopeName: "satellite/srs/telemetry", Body: []byte("{}")},
	}}
	client := dialServer(t, New(reader, nil, nil))

	resp, err := client.Fetch(context.Background(), &ocgconnectorv1.FetchRequest{
		Store: "srs/satellite/telemetry", SinceId: "01934ab2-0000-7000-8000-000000000000", Limit: 50,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n := countRecords(resp); n != 1 {
		t.Fatalf("records = %d, want 1", n)
	}
	if reader.gotStore != "srs/satellite/telemetry" || reader.gotSince != "01934ab2-0000-7000-8000-000000000000" || reader.gotLimit != 50 {
		t.Errorf("reader got store=%q since=%q limit=%d", reader.gotStore, reader.gotSince, reader.gotLimit)
	}
}

func TestFetchLogsStore(t *testing.T) {
	lg := &fakeLogs{records: []srs.Record{{ID: "01934ab2-0000-7000-8000-000000000001", ScopeName: "satellite/logs/app"}}}
	client := dialServer(t, New(&fakeReader{}, lg, nil))

	resp, err := client.Fetch(context.Background(), &ocgconnectorv1.FetchRequest{Store: "srs/logs/app", Limit: 10})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n := countRecords(resp); n != 1 {
		t.Fatalf("records = %d, want 1", n)
	}
	if lg.gotStore != "srs/logs/app" {
		t.Errorf("gotStore = %q, want %q", lg.gotStore, "srs/logs/app")
	}
}

func TestFetchSchemaVersionPassthrough(t *testing.T) {
	reader := &fakeReader{records: []srs.Record{
		{ID: "01934ab2-0000-7000-8000-000000000001", ScopeName: "satellite/srs/telemetry", Body: []byte("{}")},
	}}
	client := dialServer(t, New(reader, nil, nil))

	_, err := client.Fetch(context.Background(), &ocgconnectorv1.FetchRequest{
		Store: "srs/satellite/telemetry", SchemaVersion: "1.0.0", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if reader.gotSchemaVer != "1.0.0" {
		t.Errorf("gotSchemaVer = %q, want %q", reader.gotSchemaVer, "1.0.0")
	}
}

func TestFetchCaughtUpEmpty(t *testing.T) {
	client := dialServer(t, New(&fakeReader{records: nil}, nil, nil))
	resp, err := client.Fetch(context.Background(), &ocgconnectorv1.FetchRequest{Store: "srs/satellite/insights"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n := countRecords(resp); n != 0 {
		t.Fatalf("records = %d, want 0", n)
	}
}

func TestFetchLimitClamped(t *testing.T) {
	reader := &fakeReader{}
	client := dialServer(t, New(reader, nil, nil))

	if _, err := client.Fetch(context.Background(), &ocgconnectorv1.FetchRequest{Store: "srs/satellite/telemetry", Limit: 1_000_000}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if reader.gotLimit != DefaultMaxPageSize {
		t.Errorf("limit = %d, want clamp to %d", reader.gotLimit, DefaultMaxPageSize)
	}

	if _, err := client.Fetch(context.Background(), &ocgconnectorv1.FetchRequest{Store: "srs/satellite/telemetry", Limit: 0}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if reader.gotLimit != defaultPageSize {
		t.Errorf("limit = %d, want default %d", reader.gotLimit, defaultPageSize)
	}
}

func TestFetchUnknownStore(t *testing.T) {
	client := dialServer(t, New(&fakeReader{}, nil, nil))
	_, err := client.Fetch(context.Background(), &ocgconnectorv1.FetchRequest{Store: "bogus"})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", codeOf(err))
	}
}

func TestFetchInvalidSinceID(t *testing.T) {
	client := dialServer(t, New(&fakeReader{}, nil, nil))
	_, err := client.Fetch(context.Background(), &ocgconnectorv1.FetchRequest{Store: "srs/satellite/telemetry", SinceId: "nope"})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", codeOf(err))
	}
}

func TestFetchUnimplementedWhenNoBackend(t *testing.T) {
	client := dialServer(t, New(nil, nil, nil))
	_, err := client.Fetch(context.Background(), &ocgconnectorv1.FetchRequest{Store: "srs/satellite/telemetry"})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("code = %v, want Unimplemented", codeOf(err))
	}

	// logs store with no log backend.
	_, err = client.Fetch(context.Background(), &ocgconnectorv1.FetchRequest{Store: "srs/logs/satellite"})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("logs code = %v, want Unimplemented", codeOf(err))
	}
}

func TestFetchReaderErrorMapsToInternal(t *testing.T) {
	client := dialServer(t, New(&fakeReader{err: errors.New("db down")}, nil, nil))
	_, err := client.Fetch(context.Background(), &ocgconnectorv1.FetchRequest{Store: "srs/satellite/telemetry"})
	if codeOf(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", codeOf(err))
	}
}

func TestFetchReaderInvalidCursorMapsToInvalidArgument(t *testing.T) {
	client := dialServer(t, New(&fakeReader{err: srs.ErrInvalidCursor}, nil, nil))
	_, err := client.Fetch(context.Background(), &ocgconnectorv1.FetchRequest{Store: "srs/satellite/telemetry"})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", codeOf(err))
	}
}

func TestFetchNilRequest(t *testing.T) {
	// Call the server method directly to exercise the nil-request guard.
	srv := New(&fakeReader{}, nil, nil)
	if _, err := srv.Fetch(context.Background(), nil); codeOf(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", codeOf(err))
	}
}
