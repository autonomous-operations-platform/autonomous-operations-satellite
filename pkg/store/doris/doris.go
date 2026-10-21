package doris

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-mysql-org/go-mysql/driver"

	"github.com/autonomous-operations/aos-framework/pkg/framework/store"
	"github.com/autonomous-operations/aos-framework/pkg/uuidv7"
)

const (
	subscriberBufSize = 64

	defaultWriteBatchSize     = 500
	defaultWriteBatchInterval = 5 * time.Millisecond
	// defaultWriteBatchTimeout covers a full-size multi-row INSERT under load.
	// A 500-row INSERT is faster than 500 single-row INSERTs, so 30s is
	// conservative and matches the previous single-write deadline.
	defaultWriteBatchTimeout = 30 * time.Second
	defaultWriteChanSize     = 2048
)

type subscriber struct {
	sc     *subChan[store.TelemetryRecord]
	filter store.TelemetryFilter
}

type insightSubscriber struct {
	sc     *subChan[store.Insight]
	filter store.InsightFilter
}

type DorisStore struct {
	// mu protects only closed and the two subscriber maps.
	mu     sync.RWMutex
	closed bool

	// wg tracks in-flight Add* call invocations. Close() waits on it before
	// draining the write batchers, ensuring no send() calls are in progress
	// when the batchers are stopped.
	wg sync.WaitGroup

	subs        map[*subscriber]struct{}
	insightSubs map[*insightSubscriber]struct{}

	db  *sql.DB
	log *slog.Logger

	telBatcher *batcher[store.TelemetryRecord]
	insBatcher *batcher[store.Insight]
	raBatcher  *batcher[store.RecordedAction]
	alBatcher  *batcher[store.ActionLog]
}

// var _ ensures DorisStore satisfies the framework store contract.
var _ store.Store = (*DorisStore)(nil)

func NewStore(db *sql.DB, p Params, logger *slog.Logger) (*DorisStore, error) {
	if logger == nil {
		logger = slog.Default()
	}
	batchSize := p.WriteBatchSize
	if batchSize == 0 {
		batchSize = defaultWriteBatchSize
	}
	batchInterval := p.WriteBatchInterval
	if batchInterval == 0 {
		batchInterval = defaultWriteBatchInterval
	}
	batchTimeout := p.WriteBatchTimeout
	if batchTimeout == 0 {
		batchTimeout = defaultWriteBatchTimeout
	}
	chanSize := p.WriteChanSize
	if chanSize == 0 {
		chanSize = defaultWriteChanSize
	}
	log := logger.With("component", "store")
	s := &DorisStore{
		subs:        make(map[*subscriber]struct{}),
		insightSubs: make(map[*insightSubscriber]struct{}),
		db:          db,
		log:         log,
	}
	s.telBatcher = newBatcher("telemetry", batchSize, batchInterval, batchTimeout, chanSize, s.flushTelemetry, log)
	s.insBatcher = newBatcher("insights", batchSize, batchInterval, batchTimeout, chanSize, s.flushInsights, log)
	s.raBatcher = newBatcher("recorded_actions", batchSize, batchInterval, batchTimeout, chanSize, s.flushRecordedActions, log)
	s.alBatcher = newBatcher("action_logs", batchSize, batchInterval, batchTimeout, chanSize, s.flushActionLogs, log)
	if err := s.initSchema(); err != nil {
		s.telBatcher.close()
		s.insBatcher.close()
		s.raBatcher.close()
		s.alBatcher.close()
		return nil, fmt.Errorf("doris: init schema: %w", err)
	}
	return s, nil
}

func (s *DorisStore) initSchema() error {
	stmts := []string{
		// Drop and recreate tables to apply schema changes. Safe for dev since
		// Docker volumes are ephemeral; add a migration gate if preserving data.
		`DROP TABLE IF EXISTS telemetry`,
		`DROP TABLE IF EXISTS insights`,
		`DROP TABLE IF EXISTS recorded_actions`,
		`DROP TABLE IF EXISTS action_logs`,

		`CREATE TABLE IF NOT EXISTS telemetry (
			id           VARCHAR(36)  NOT NULL,
			signal       VARCHAR(50)  NOT NULL,
			payload      STRING       NOT NULL,
			attributes   VARIANT      NOT NULL,
			received_at  DATETIME(3)  NOT NULL
		) DUPLICATE KEY(id)
		DISTRIBUTED BY HASH(id) BUCKETS 3
		PROPERTIES ("replication_num" = "1")`,

		`CREATE TABLE IF NOT EXISTS insights (
			id                VARCHAR(36)  NOT NULL,
			observer_name     VARCHAR(255) NOT NULL,
			pattern           VARCHAR(255) NOT NULL,
			severity          VARCHAR(50)  NOT NULL,
			summary           STRING       NOT NULL,
			window_start      DATETIME(3)  NOT NULL,
			window_end        DATETIME(3)  NOT NULL,
			created_at        DATETIME(3)  NOT NULL,
			source_record_ids STRING       NOT NULL DEFAULT '[]'
		) DUPLICATE KEY(id)
		DISTRIBUTED BY HASH(id) BUCKETS 3
		PROPERTIES ("replication_num" = "1")`,

		`CREATE TABLE IF NOT EXISTS recorded_actions (
			id              VARCHAR(36)  NOT NULL,
			execution_id    VARCHAR(36)  NOT NULL,
			insight_id      VARCHAR(36)  NOT NULL,
			operator_name   VARCHAR(255) NOT NULL,
			state           VARCHAR(50)  NOT NULL,
			timestamp       DATETIME(3)  NOT NULL
		) DUPLICATE KEY(id)
		DISTRIBUTED BY HASH(id) BUCKETS 3
		PROPERTIES ("replication_num" = "1")`,

		// action_logs holds individual operator-execution steps linked to their
		// recorded_actions rows by execution_id. No ring buffer or
		// subscribe channel: nothing consumes action logs live; Doris durability
		// alone satisfies the "observable after a crash" requirement.
		`CREATE TABLE IF NOT EXISTS action_logs (
			id           VARCHAR(36)  NOT NULL,
			execution_id VARCHAR(36)  NOT NULL,
			step_id      VARCHAR(255) NOT NULL DEFAULT '',
			description  STRING       NOT NULL DEFAULT '',
			state        VARCHAR(50)  NOT NULL,
			timestamp    DATETIME(3)  NOT NULL
		) DUPLICATE KEY(id)
		DISTRIBUTED BY HASH(execution_id) BUCKETS 3
		PROPERTIES ("replication_num" = "1")`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("exec DDL: %w", err)
		}
	}
	return nil
}

func (s *DorisStore) AddTelemetry(ctx context.Context, records []store.TelemetryRecord) error {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return store.ErrStoreClosed
	}
	s.wg.Add(1)
	s.mu.RUnlock()
	defer s.wg.Done()

	for _, rec := range records {
		s.telBatcher.send(rec)
	}

	// Re-acquire a read lock for fan-out so this window races against the
	// write lock held by Close() and context-cancel unsubscribes. This
	// prevents sending on a closed channel. All sends have a default branch
	// so we never block while holding the read lock.
	s.mu.RLock()
	for sub := range s.subs {
		for _, rec := range records {
			if !sub.filter.Matches(rec) {
				continue
			}
			sub.sc.send(rec)
		}
	}
	s.mu.RUnlock()

	return nil
}

func (s *DorisStore) flushTelemetry(ctx context.Context, batch []store.TelemetryRecord) error {
	numArgs := 5
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", numArgs), ",") + ")"
	placeholders := strings.TrimSuffix(strings.Repeat(row+",", len(batch)), ",")
	args := make([]any, 0, len(batch)*numArgs)
	for _, rec := range batch {
		attrs, _ := json.Marshal(rec.Attributes)
		if len(rec.Attributes) == 0 {
			// A nil map marshals to "null"; the VARIANT column is NOT NULL and
			// expects a JSON object.
			attrs = []byte("{}")
		}
		args = append(args,
			string(rec.ID), string(rec.Signal), string(rec.Payload),
			string(attrs), timeArg(rec.ReceivedAt),
		)
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO telemetry (id, signal, payload, attributes, received_at) VALUES "+placeholders, //nolint:gosec // G202: placeholders are synthesized, no user input
		args...,
	)
	if err != nil {
		for _, rec := range batch {
			s.log.Error("doris write telemetry failed", "id", rec.ID)
		}
		return err
	}
	return nil
}

func (s *DorisStore) SubscribeTelemetry(ctx context.Context, filter store.TelemetryFilter) (<-chan store.TelemetryRecord, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, store.ErrStoreClosed
	}
	sub := &subscriber{sc: newSubChan[store.TelemetryRecord](), filter: filter}
	s.subs[sub] = struct{}{}
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		delete(s.subs, sub)
		sub.sc.closeOnce()
		drops := sub.sc.Drops()
		s.mu.Unlock()
		if drops > 0 {
			s.log.Warn("subscriber dropped records (back-pressure)", "drops", drops)
		}
	}()

	return sub.sc.C(), nil
}

func (s *DorisStore) QueryTelemetryByTime(ctx context.Context, q store.TelemetryTimeRangeQuery) (store.QueryResult[store.TelemetryRecord], error) {
	if err := q.Validate(); err != nil {
		return store.QueryResult[store.TelemetryRecord]{}, fmt.Errorf("%w: %v", store.ErrInvalidQuery, err)
	}

	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return store.QueryResult[store.TelemetryRecord]{}, store.ErrStoreClosed
	}

	qry := "SELECT id, signal, payload, attributes, received_at FROM telemetry WHERE 1=1"
	args := []any{}
	if !q.Start.IsZero() {
		qry += " AND received_at >= ?"
		args = append(args, timeArg(q.Start))
	}
	if !q.End.IsZero() {
		qry += " AND received_at < ?"
		args = append(args, timeArg(q.End))
	}
	if conds, cargs := attrConds(q.Attributes); len(conds) > 0 {
		qry += " AND " + strings.Join(conds, " AND ")
		args = append(args, cargs...)
	}
	qry += " ORDER BY received_at ASC"

	rows, err := s.db.QueryContext(ctx, qry, args...)
	if err != nil {
		return store.QueryResult[store.TelemetryRecord]{}, fmt.Errorf("doris QueryTelemetryByTime: %w", err)
	}
	defer rows.Close()

	var records []store.TelemetryRecord
	for rows.Next() {
		rec, err := scanTelemetry(rows)
		if err != nil {
			return store.QueryResult[store.TelemetryRecord]{}, err
		}
		if attrsMatch(rec.Attributes, q.Attributes) {
			records = append(records, rec)
		}
	}
	if err := rows.Err(); err != nil {
		return store.QueryResult[store.TelemetryRecord]{}, fmt.Errorf("doris QueryTelemetryByTime rows: %w", err)
	}
	return store.QueryResult[store.TelemetryRecord]{Records: records}, nil
}

func (s *DorisStore) QueryTelemetryFromCursor(ctx context.Context, q store.TelemetryCursorQuery) (store.QueryResult[store.TelemetryRecord], error) {
	if err := q.Validate(); err != nil {
		return store.QueryResult[store.TelemetryRecord]{}, fmt.Errorf("%w: %v", store.ErrInvalidQuery, err)
	}
	if q.AfterID != "" && !uuidv7.IsValid(string(q.AfterID)) {
		return store.QueryResult[store.TelemetryRecord]{}, store.ErrInvalidID
	}

	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return store.QueryResult[store.TelemetryRecord]{}, store.ErrStoreClosed
	}

	qry := "SELECT id, signal, payload, attributes, received_at FROM telemetry"
	var conds []string
	args := []any{}
	if q.AfterID != "" {
		conds = append(conds, "id > ?")
		args = append(args, string(q.AfterID))
	}
	if ac, cargs := attrConds(q.Attributes); len(ac) > 0 {
		conds = append(conds, ac...)
		args = append(args, cargs...)
	}
	if len(conds) > 0 {
		qry += " WHERE " + strings.Join(conds, " AND ")
	}
	qry += " ORDER BY id ASC LIMIT ?"
	args = append(args, q.Count+1)

	rows, err := s.db.QueryContext(ctx, qry, args...)
	if err != nil {
		return store.QueryResult[store.TelemetryRecord]{}, fmt.Errorf("doris QueryTelemetryFromCursor: %w", err)
	}
	defer rows.Close()

	var matched []store.TelemetryRecord
	for rows.Next() {
		rec, err := scanTelemetry(rows)
		if err != nil {
			return store.QueryResult[store.TelemetryRecord]{}, err
		}
		if attrsMatch(rec.Attributes, q.Attributes) {
			matched = append(matched, rec)
		}
	}
	if err := rows.Err(); err != nil {
		return store.QueryResult[store.TelemetryRecord]{}, fmt.Errorf("doris QueryTelemetryFromCursor rows: %w", err)
	}

	hasMore := len(matched) > q.Count
	if hasMore {
		matched = matched[:q.Count]
	}
	var nextCursor store.RecordID
	if hasMore && len(matched) > 0 {
		nextCursor = matched[len(matched)-1].ID
	}
	return store.QueryResult[store.TelemetryRecord]{Records: matched, NextCursor: nextCursor}, nil
}

func (s *DorisStore) AddInsight(ctx context.Context, insight store.Insight) error {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return store.ErrStoreClosed
	}
	s.wg.Add(1)
	s.mu.RUnlock()
	defer s.wg.Done()

	s.insBatcher.send(insight)

	s.mu.RLock()
	for sub := range s.insightSubs {
		if !sub.filter.Matches(insight) {
			continue
		}
		sub.sc.send(insight)
	}
	s.mu.RUnlock()

	return nil
}

func (s *DorisStore) flushInsights(ctx context.Context, batch []store.Insight) error {
	numArgs := 9
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", numArgs), ",") + ")"
	placeholders := strings.TrimSuffix(strings.Repeat(row+",", len(batch)), ",")
	args := make([]any, 0, len(batch)*numArgs)
	for _, ins := range batch {
		sourceIDs := make([]string, len(ins.SourceRecordIDs))
		for i, id := range ins.SourceRecordIDs {
			sourceIDs[i] = string(id)
		}
		sourceIDsJSON, _ := json.Marshal(sourceIDs)
		args = append(args,
			string(ins.ID), ins.ObserverName, ins.Pattern, string(ins.Severity),
			ins.Summary, timeArg(ins.WindowStart), timeArg(ins.WindowEnd), timeArg(ins.CreatedAt),
			string(sourceIDsJSON),
		)
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO insights (id, observer_name, pattern, severity, summary, window_start, window_end, created_at, source_record_ids) VALUES "+placeholders, //nolint:gosec // G202: placeholders are synthesized, no user input
		args...,
	)
	if err != nil {
		for _, ins := range batch {
			s.log.Error("doris write insight failed", "id", ins.ID, "observer", ins.ObserverName)
		}
		return err
	}
	return nil
}

func (s *DorisStore) SubscribeInsights(ctx context.Context, filter store.InsightFilter) (<-chan store.Insight, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, store.ErrStoreClosed
	}
	sub := &insightSubscriber{sc: newSubChan[store.Insight](), filter: filter}
	s.insightSubs[sub] = struct{}{}
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		delete(s.insightSubs, sub)
		sub.sc.closeOnce()
		s.mu.Unlock()
	}()

	return sub.sc.C(), nil
}

func (s *DorisStore) AddRecordedAction(ctx context.Context, ra store.RecordedAction) error {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return store.ErrStoreClosed
	}
	s.wg.Add(1)
	s.mu.RUnlock()
	defer s.wg.Done()

	s.raBatcher.send(ra)
	return nil
}

func (s *DorisStore) flushRecordedActions(ctx context.Context, batch []store.RecordedAction) error {
	numArgs := 6
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", numArgs), ",") + ")"
	placeholders := strings.TrimSuffix(strings.Repeat(row+",", len(batch)), ",")
	args := make([]any, 0, len(batch)*numArgs)
	for _, ra := range batch {
		args = append(args,
			string(ra.ID), string(ra.ExecutionID), string(ra.InsightID),
			ra.OperatorName, string(ra.State), timeArg(ra.Timestamp),
		)
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO recorded_actions (id, execution_id, insight_id, operator_name, state, timestamp) VALUES "+placeholders, //nolint:gosec // G202: placeholders are synthesized, no user input
		args...,
	)
	if err != nil {
		for _, ra := range batch {
			s.log.Error("doris write recorded action failed", "id", ra.ID, "operator", ra.OperatorName)
		}
		return err
	}
	return nil
}

func (s *DorisStore) AddActionLog(ctx context.Context, al store.ActionLog) error {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return store.ErrStoreClosed
	}
	s.wg.Add(1)
	s.mu.RUnlock()
	defer s.wg.Done()

	s.alBatcher.send(al)
	return nil
}

func (s *DorisStore) flushActionLogs(ctx context.Context, batch []store.ActionLog) error {
	numArgs := 6
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", numArgs), ",") + ")"
	placeholders := strings.TrimSuffix(strings.Repeat(row+",", len(batch)), ",")
	args := make([]any, 0, len(batch)*numArgs)
	for _, al := range batch {
		args = append(args,
			string(al.ID), string(al.ExecutionID), al.StepID,
			al.Description, string(al.State), timeArg(al.Timestamp),
		)
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO action_logs (id, execution_id, step_id, description, state, timestamp) VALUES "+placeholders, //nolint:gosec // G202: placeholders are synthesized, no user input
		args...,
	)
	if err != nil {
		for _, al := range batch {
			s.log.Error("doris write action log failed", "id", al.ID, "execution_id", al.ExecutionID)
		}
		return err
	}
	return nil
}

func (s *DorisStore) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for sub := range s.subs {
		sub.sc.closeOnce()
		delete(s.subs, sub)
	}
	for sub := range s.insightSubs {
		sub.sc.closeOnce()
		delete(s.insightSubs, sub)
	}
	s.mu.Unlock()
	// Wait for all in-flight Add* calls to finish sending to the batchers.
	s.wg.Wait()
	// Drain and stop each write batcher before closing the DB connection.
	s.telBatcher.close()
	s.insBatcher.close()
	s.raBatcher.close()
	s.alBatcher.close()
	s.db.Close()
}

func scanTelemetry(rows *sql.Rows) (store.TelemetryRecord, error) {
	var rec store.TelemetryRecord
	var signal, attrsJSON, payload string
	var receivedAt dorisTime
	if err := rows.Scan(&rec.ID, &signal, &payload, &attrsJSON, &receivedAt); err != nil {
		return rec, fmt.Errorf("doris scan telemetry: %w", err)
	}
	rec.Signal = store.Signal(signal)
	rec.Payload = []byte(payload)
	rec.Attributes = flattenAttrs(attrsJSON)
	rec.ReceivedAt = receivedAt.t
	return rec, nil
}

var attrKeyRE = regexp.MustCompile(`^[A-Za-z0-9_.:/-]+$`)

func attrConds(attrs map[string]string) ([]string, []any) {
	var conds []string
	var args []any
	for k, v := range attrs {
		if !attrKeyRE.MatchString(k) {
			continue
		}
		conds = append(conds, "CAST(attributes['"+k+"'] AS STRING) = ?")
		args = append(args, v)
	}
	return conds, args
}

func flattenAttrs(raw string) map[string]string {
	out := map[string]string{}
	var nested map[string]any
	if err := json.Unmarshal([]byte(raw), &nested); err != nil {
		return out
	}
	flattenInto("", nested, out)
	return out
}

func flattenInto(prefix string, m map[string]any, out map[string]string) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch t := v.(type) {
		case map[string]any:
			flattenInto(key, t, out)
		case string:
			out[key] = t
		case float64:
			out[key] = strconv.FormatFloat(t, 'f', -1, 64)
		case bool:
			out[key] = strconv.FormatBool(t)
		case nil:
			// skip JSON nulls
		default:
			b, _ := json.Marshal(t)
			out[key] = string(b)
		}
	}
}

func attrsMatch(recAttrs, filterAttrs map[string]string) bool {
	for k, v := range filterAttrs {
		if recAttrs[k] != v {
			return false
		}
	}
	return true
}
