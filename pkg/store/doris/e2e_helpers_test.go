//go:build e2e

package doris

import (
	"database/sql"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/autonomous-operations/aos-framework/pkg/framework/store"
	"github.com/autonomous-operations/aos-framework/pkg/uuidv7"
)

// waitForDoris retries until Doris accepts connections and can execute DDL.
// The BE needs ~60–90 s to register with the FE after container startup;
// a successful CREATE TABLE is the reliable readiness signal.
func waitForDoris() error {
	deadline := time.Now().Add(e2eReadyTimeout)
	var last error
	for time.Now().Before(deadline) {
		if last = probeDoris(); last == nil {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("Doris not ready after %s: %w", e2eReadyTimeout, last)
}

func probeDoris() error {
	admin, err := sql.Open("mysql", dsn(e2eParams, ""))
	if err != nil {
		return err
	}
	defer admin.Close()
	if err := admin.Ping(); err != nil {
		return err
	}
	if _, err := admin.Exec("CREATE DATABASE IF NOT EXISTS `" + e2eParams.DB + "`"); err != nil {
		return err
	}

	db, err := sql.Open("mysql", dsn(e2eParams, e2eParams.DB))
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS _probe (id VARCHAR(1))
		DUPLICATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1
		PROPERTIES ("replication_num"="1")`)
	if err != nil {
		return err
	}
	_, _ = db.Exec(`DROP TABLE IF EXISTS _probe`)
	return nil
}

// freePort asks the OS for an available TCP port on localhost.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// openVerifyDB returns a raw *sql.DB for post-Close verification.
// It does NOT call NewStore, so it never drops or recreates tables.
func openVerifyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDB(e2eParams)
	if err != nil {
		t.Fatalf("openVerifyDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newTelemetryRecord() store.TelemetryRecord {
	return store.TelemetryRecord{
		ID:         store.RecordID(uuidv7.Generate()),
		Signal:     "e2e.test",
		Payload:    []byte(`{"value":1}`),
		Attributes: map[string]string{"env": "test"},
		ReceivedAt: time.Now().UTC(),
	}
}

func newInsight() store.Insight {
	now := time.Now().UTC()
	return store.Insight{
		ID:           store.RecordID(uuidv7.Generate()),
		ObserverName: "e2e-observer",
		Pattern:      "e2e-pattern",
		Severity:     store.SeverityWarning,
		Summary:      "e2e test insight",
		WindowStart:  now.Add(-time.Minute),
		WindowEnd:    now,
		CreatedAt:    now,
	}
}

func newRecordedAction() store.RecordedAction {
	return store.RecordedAction{
		ID:           store.RecordID(uuidv7.Generate()),
		ExecutionID:  store.RecordID(uuidv7.Generate()),
		InsightID:    store.RecordID(uuidv7.Generate()),
		OperatorName: "e2e-operator",
		State:        store.StateStarted,
		Timestamp:    time.Now().UTC(),
	}
}

func newActionLog() store.ActionLog {
	return store.ActionLog{
		ID:          store.RecordID(uuidv7.Generate()),
		ExecutionID: store.RecordID(uuidv7.Generate()),
		StepID:      "e2e-step",
		Description: "e2e test action log",
		State:       store.ActionLogStateInfo,
		Timestamp:   time.Now().UTC(),
	}
}
