//go:build e2e

package doris

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

const (
	e2eImage        = "apache/doris:all-in-one-4.1.3"
	e2eReadyTimeout = 120 * time.Second
	// e2eLabel marks every container this harness creates, so stragglers from
	// an interrupted run can be reaped without touching other Doris containers.
	e2eLabel = "aos-e2e=1"
)

// e2eParams is set by runE2E before any test runs.
var e2eParams Params

func TestMain(m *testing.M) {
	os.Exit(runE2E(m))
}

func runE2E(m *testing.M) int {
	// Reuse an already-running Doris when DORIS_E2E_ADDR is set, so the same
	// tests can run against a warm backend instead of a freshly started
	// (cold) container. Format: host:port, e.g. 127.0.0.1:9030.
	if addr := os.Getenv("DORIS_E2E_ADDR"); addr != "" {
		e2eParams = Params{Addr: addr, User: "root", Password: "", DB: "aos_e2e", MaxOpenConns: 5}
		if err := waitForDoris(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return m.Run()
	}

	// A fresh container is required: reap any left behind by an interrupted
	// previous run (an aborted `go test` skips the defer below) before
	// starting a new one. Match by label so unrelated Doris containers (for
	// example a developer's warm backend) are never touched.
	reapStaleE2EContainers()

	port, err := freePort()
	if err != nil {
		fmt.Fprintf(os.Stderr, "find free port: %v\n", err)
		return 1
	}

	name := fmt.Sprintf("doris-aos-e2e-%d", time.Now().UnixNano())

	out, err := exec.Command("docker", "run", "-d",
		"--name", name,
		"--label", e2eLabel,
		"-p", fmt.Sprintf("%d:9030", port),
		"-e", "FE_CONFIG_EXTRA=sys_log_level = WARN",
		"-e", "BE_CONFIG_EXTRA=sys_log_level = WARN",
		"--ulimit", "nofile=65536:65536",
		e2eImage,
	).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker run: %v\n%s\n", err, out)
		return 1
	}
	defer exec.Command("docker", "rm", "-f", name).Run()

	e2eParams = Params{
		Addr:         fmt.Sprintf("127.0.0.1:%d", port),
		User:         "root",
		Password:     "",
		DB:           "aos_e2e",
		MaxOpenConns: 5,
	}

	if err := waitForDoris(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	return m.Run()
}

// reapStaleE2EContainers force-removes any containers this harness created in a
// previous run that were not cleaned up (for example because the run was
// interrupted). It matches only on the e2e label, so other Doris containers are
// left alone. Errors are ignored: a missing docker or no matches is not fatal.
func reapStaleE2EContainers() {
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label="+e2eLabel).Output()
	if err != nil {
		return
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return
	}
	_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
}

func TestE2E_WriteTelemetry(t *testing.T) {
	ctx := context.Background()

	s, err := Connect(ctx, e2eParams, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTelemetry(ctx, []store.TelemetryRecord{newTelemetryRecord()}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	db := openVerifyDB(t)
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM telemetry").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 telemetry record, got %d", count)
	}
}

// TestE2E_ConnectThenImmediateWrite writes a single record immediately after
// Connect() returns, then reads it back. A Store returned by Connect() must
// accept writes right away; if the record is missing afterward, Connect()
// handed back a store that was not yet ready to persist writes.
func TestE2E_ConnectThenImmediateWrite(t *testing.T) {
	ctx := context.Background()

	s, err := Connect(ctx, e2eParams, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	rec := newTelemetryRecord()
	if err := s.AddTelemetry(ctx, []store.TelemetryRecord{rec}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	db := openVerifyDB(t)
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM telemetry WHERE id = ?", string(rec.ID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("record written immediately after Connect() did not persist (count=%d)", count)
	}
}

func TestE2E_WriteInsight(t *testing.T) {
	ctx := context.Background()

	s, err := Connect(ctx, e2eParams, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddInsight(ctx, newInsight()); err != nil {
		t.Fatal(err)
	}
	s.Close()

	db := openVerifyDB(t)
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM insights").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 insight, got %d", count)
	}
}

func TestE2E_WriteRecordedAction(t *testing.T) {
	ctx := context.Background()

	s, err := Connect(ctx, e2eParams, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddRecordedAction(ctx, newRecordedAction()); err != nil {
		t.Fatal(err)
	}
	s.Close()

	db := openVerifyDB(t)
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recorded_actions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 recorded action, got %d", count)
	}
}

func TestE2E_WriteActionLog(t *testing.T) {
	ctx := context.Background()

	s, err := Connect(ctx, e2eParams, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddActionLog(ctx, newActionLog()); err != nil {
		t.Fatal(err)
	}
	s.Close()

	db := openVerifyDB(t)
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM action_logs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 action log, got %d", count)
	}
}

// TestE2E_CloseWaitsForInflightWrites verifies that after Close() returns,
// every record passed to AddTelemetry has persisted. Close() must not return
// until all accepted writes are durable; otherwise records added just before
// Close are silently lost.
func TestE2E_CloseWaitsForInflightWrites(t *testing.T) {
	ctx := context.Background()

	s, err := Connect(ctx, e2eParams, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	const n = 50
	recs := make([]store.TelemetryRecord, n)
	for i := range recs {
		recs[i] = newTelemetryRecord()
	}

	if err := s.AddTelemetry(ctx, recs); err != nil {
		t.Fatal(err)
	}
	s.Close() // must not return until all accepted writes are durable

	db := openVerifyDB(t)
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM telemetry").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Errorf("records added before Close() were lost: expected %d, got %d", n, count)
	}
}

// TestE2E_SustainedWriteLoad drives sustained write load into each of the four
// record types in turn, then checks that every record persisted. A Store must
// durably persist every record it accepts; a row missing after Close has been
// lost. Each type is loaded and verified independently so a failure names the
// table that dropped writes.
func TestE2E_SustainedWriteLoad(t *testing.T) {
	ctx := context.Background()

	// Discard the per-write error logs; a dropped write shows up as a missing
	// row in the final count, which is what we assert on.
	s, err := Connect(ctx, e2eParams, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	// Each case loads one table. emit dispatches n records of that table's type
	// through the Store interface. Connect recreates every table empty, so a
	// plain row count equals the number of accepted writes.
	cases := []struct {
		table string
		emit  func(n int) error
	}{
		{"telemetry", func(n int) error {
			recs := make([]store.TelemetryRecord, n)
			for i := range recs {
				recs[i] = newTelemetryRecord()
			}
			return s.AddTelemetry(ctx, recs)
		}},
		{"insights", func(n int) error {
			for range n {
				if err := s.AddInsight(ctx, newInsight()); err != nil {
					return err
				}
			}
			return nil
		}},
		{"recorded_actions", func(n int) error {
			for range n {
				if err := s.AddRecordedAction(ctx, newRecordedAction()); err != nil {
					return err
				}
			}
			return nil
		}},
		{"action_logs", func(n int) error {
			for range n {
				if err := s.AddActionLog(ctx, newActionLog()); err != nil {
					return err
				}
			}
			return nil
		}},
	}

	sent := make(map[string]int, len(cases))
	for _, c := range cases {
		sent[c.table] = sustainedDispatch(t, c.emit)
	}

	s.Close() // blocks until every accepted write is durable

	db := openVerifyDB(t)
	for _, c := range cases {
		var persisted int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+c.table).Scan(&persisted); err != nil {
			t.Fatalf("count %s: %v", c.table, err)
		}
		want := sent[c.table]
		t.Logf("%s: dispatched %d, persisted %d (dropped %d)", c.table, want, persisted, want-persisted)
		if persisted != want {
			t.Errorf("%s dropped writes: dispatched %d, persisted %d", c.table, want, persisted)
		}
	}
}

// sustainedDispatch drives writes at a fixed rate for a fixed duration by
// calling emit once per tick with the batch size needed to hit the rate. emit
// abstracts over which table the records target, so one loop loads any type. It
// returns the total number of records dispatched.
func sustainedDispatch(t *testing.T, emit func(n int) error) int {
	t.Helper()
	const (
		ratePerSec    = 1000
		duration      = 5 * time.Second
		batchInterval = 10 * time.Millisecond // 100 batches/sec
		perBatch      = ratePerSec / 100      // records per batch to hit the rate
	)

	tick := time.NewTicker(batchInterval)
	defer tick.Stop()
	deadline := time.After(duration)
	sent := 0
	for {
		select {
		case <-deadline:
			return sent
		case <-tick.C:
			if err := emit(perBatch); err != nil {
				t.Fatal(err)
			}
			sent += perBatch
		}
	}
}
