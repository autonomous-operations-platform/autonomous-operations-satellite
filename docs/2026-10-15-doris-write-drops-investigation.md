# Doris write-drop investigation

## Summary

A satellite process using this framework silently dropped telemetry writes
under sustained ingestion. The logs filled with `context deadline exceeded` /
`doris write telemetry failed`. This document records the symptom, the theories
we tested, and the one that holds.

Key conclusion: **the drops are load-driven, not tablet-init or
connection-staleness.** `AddTelemetry` is fire-and-forget — it spawns one
goroutine per record. At a sustained ingest rate those goroutines queue against
a small connection pool while Doris executes slow single-row INSERTs. Writes
that cannot acquire a connection before the 30-second write deadline fire are
dropped, and the drop is invisible to the caller. The same pool starvation
stalls concurrent reads (for example, an observer querying Doris), which is why
"nothing happens" even though telemetry is being received.

The reproduction lives in the test suite: `TestE2E_SustainedWriteLoad`
(`e2e_test.go`). The theories we discarded are kept below so the reasoning is
not re-done later.

## Observed symptom

1. A Doris Docker container runs and is healthy. It was started from an
   existing (warm) container and had been up for several minutes.
2. The satellite starts. It connects, which re-initializes the tables
   (`DROP TABLE` + `CREATE TABLE` on every start, see `initSchema`).
3. The HTTP server immediately receives telemetry and calls `AddTelemetry`.
4. A few seconds after the framework reports "up and running", specific logs
   that should trigger the observer and operator are sent.
5. Nothing happens. Later, the logs fill with `context deadline exceeded` and
   `doris write telemetry failed`.

## Relevant configuration (facts, measured 2026-10-15)

Connection pool (`connect.go`, `openDB`):

| Setting | Value |
| --- | --- |
| `MaxOpenConns` | 5 (`defaultMaxOpenConns`, configurable via `Params`) |
| `MaxIdleConns` | equal to `MaxOpenConns` |
| `ConnMaxLifetime` | 5 minutes |
| `ConnMaxIdleTime` | 2 minutes |

Async write deadline (`doris.go`): `dorisWriteTimeout = 30 * time.Second`.

Doris server session timeouts, queried from the running container
(`apache/doris:all-in-one-4.1.3`) with `SHOW VARIABLES LIKE '%timeout%'`:

```
interactive_timeout      = 3600     # 1 hour
net_read_timeout         = 600      # 10 minutes
net_write_timeout        = 600      # 10 minutes
wait_timeout             = 28800    # 8 hours
```

## How writes work

`AddTelemetry` is fire-and-forget. It spawns one goroutine per record and
returns `nil` immediately. It never surfaces a write error to the caller. Each
write runs under the 30-second deadline; on any error, including a deadline, it
logs and drops the record (`doris.go`):

```go
func (s *DorisStore) AddTelemetry(ctx context.Context, records []store.TelemetryRecord) error {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return store.ErrStoreClosed
	}
	s.wg.Add(len(records))
	s.mu.RUnlock()

	for _, rec := range records {
		go func() {
			defer s.wg.Done()
			s.writeTelemetryToDoris(ctx, rec) // error is logged, not returned
		}()
	}
	// ... fan-out to in-memory subscribers ...
	return nil
}

func (s *DorisStore) writeTelemetryToDoris(ctx context.Context, rec store.TelemetryRecord) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dorisWriteTimeout)
	defer cancel()
	// ... INSERT ...
	if err != nil {
		s.log.Error("doris write telemetry failed", "err", err, "id", rec.ID)
		return // record is silently dropped
	}
}
```

Consequence: a failed write is invisible to the caller. It shows up only as a
missing row and an error log line. There is no cap on the number of in-flight
write goroutines and no batching — N records become N goroutines competing for
at most `MaxOpenConns` connections.

## Confirmed root cause: load-driven drops

`TestE2E_SustainedWriteLoad` ingests telemetry at 1000 writes/second for five
seconds against a ready backend, then counts how many rows persisted. Result:

```
dispatched 5000 records at 1000/s; persisted ~2400 (dropped ~2600)
```

Roughly half the records are lost. The mechanism:

- 1000 records/second become 1000 goroutines/second, each issuing a single-row
  INSERT.
- The pool has 5 connections. Doris single-row INSERTs are slow (each creates a
  new data version per tablet), so connections do not turn over fast enough.
- Goroutines block waiting for a connection. The backlog grows faster than it
  drains. Once a goroutine's wait plus execution exceeds `dorisWriteTimeout`,
  its context deadline fires and the record is dropped.
- Any other query sharing the pool — an observer reading telemetry — waits
  behind the same backlog and times out too. This matches the "pub/sub
  delivered the record but the observer never acted" observation: the observer
  reads Doris, and the read cannot get a connection.

This is a throughput/concurrency problem, independent of how warm the backend
is.

## Discarded and secondary theories

### Tablet-init latency (discarded)

Theory: Doris creates tablets asynchronously after `CREATE TABLE` returns, so
writes in the first moments after start fail until tablets are ready.

Measured and rejected. We re-created the `telemetry` table exactly as
`initSchema` does (`DROP TABLE` then `CREATE TABLE ... DISTRIBUTED BY HASH(id)
BUCKETS 3`), then timed single-row INSERTs:

```sql
INSERT INTO telemetry (id, signal, payload, attributes, received_at)
VALUES (?, '_m', '{}', '{}', NOW());
```

On a warm backend, `CREATE TABLE` returns in ~75 ms, a cold burst of 5
concurrent INSERTs lands in ~300 ms each, and sequential INSERTs reach steady
state (<100 ms) about 600 ms after `CREATE TABLE`:

```
CREATE TABLE returned after 76ms
cold burst (5 concurrent):   ~300ms each
steady state (<100ms x3):    ~607ms after CREATE TABLE
```

That is two orders of magnitude below the 30-second deadline, so tablet init
cannot by itself cause a 30-second `context deadline exceeded`.

### Cold backend with no registered BE (real, but separate)

Start a brand-new container and connect the instant the FE is reachable:

```sh
NAME="doris-cold-$(date +%s)"
docker run -d --name "$NAME" -p 19030:9030 \
  -e FE_CONFIG_EXTRA="sys_log_level = WARN" \
  -e BE_CONFIG_EXTRA="sys_log_level = WARN" \
  --ulimit nofile=65536:65536 \
  apache/doris:all-in-one-4.1.3
```

The FE accepts a MySQL-protocol connection after ~7 seconds, but the first
`CREATE DATABASE` fails with:

```
ERROR 1105 (HY000): errCode = 2, detailMessage = System has no available disk capacity or no available BE nodes
```

A cold backend accepts connections before the BE registers; during that window
DDL and writes fail. `Connect()` already retries DDL (the `openDB` and
`NewStore` loops), so it survives this. This is a start-up timing issue, not the
warm-container symptom.

### Stale pooled connection (plausible, not the primary cause)

A pooled TCP connection can go idle and become unusable while the Go
`database/sql` pool still believes it is good (TCP keepalive expiry, or the
Docker/WSL2 NAT table evicting an idle flow). The server-side `wait_timeout` is
8 hours, so the FE is not the one closing it. The next write on a half-open
socket blocks until the context deadline fires.

Lowering `ConnMaxIdleTime` narrows this window, but it does not address the
load-driven drops: under sustained ingest the connections are busy, not idle,
so staleness is not what fails them. The current setting is 2 minutes.

### Use-after-close race (real, addressed)

`Close()` could close the DB pool while async write goroutines were still
running; those goroutines then called `ExecContext` on a closed pool and failed.
A `sync.WaitGroup` now tracks in-flight write goroutines: each `AddX` adds
before spawning, each goroutine calls `Done` on exit, and `Close()` calls
`wg.Wait()` before `db.Close()` (`doris.go`). This removes the race but is
orthogonal to the load drops.

## Fix direction

The load drops need the write path to stop issuing one unbounded goroutine and
one single-row INSERT per record. Options, roughly in order of impact:

1. **Batch writes.** Accumulate records over a short window (or up to a size)
   and issue multi-row INSERTs. This cuts both the number of statements and the
   per-tablet version churn, which is the dominant cost in Doris.
2. **Bound concurrency.** Cap in-flight writers (worker pool sized to the
   connection pool) so goroutines do not pile up unboundedly.
3. **Apply back-pressure or surface drops.** `AddTelemetry` currently hides
   failures. At minimum, count drops; better, signal when the pipeline is
   saturated instead of silently discarding.

`TestE2E_SustainedWriteLoad` is the regression check: it must persist every
dispatched record.

## Reproduction checklist

1. Run the load test: `go test -tags e2e -run TestE2E_SustainedWriteLoad -count=1 -v ./pkg/store/doris/`.
   Expect dropped records against the current implementation.
2. Rule out tablet init: re-create the `telemetry` table and time single-row
   INSERTs against a warm backend. Expect sub-second steady state.
3. Query server timeouts: `SHOW VARIABLES LIKE '%timeout%'`. Confirm
   `wait_timeout` is far larger than `ConnMaxIdleTime`.
4. Confirm `AddTelemetry` does not surface write errors — a dropped write is
   visible only as a missing row plus an error log.
