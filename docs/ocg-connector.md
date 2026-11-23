# OCG Connector

The `ocg-connector` is a separate binary in the Runtime Bundle. It reads
Satellite Record Store (SRS) records directly from Apache Doris and scans log
files on demand per Fetch request. The satellite binary is unchanged.

## Structure

```
cmd/ocg-connector/main.go          entry point — flags, wiring, graceful shutdown
pkg/ocg-connector/
  server/server.go                 gRPC Fetch handler — validates, clamps, dispatches
  server/ratelimit.go              token-bucket unary interceptor
  srs/reader.go                    cursor-based Doris reads for the four SRS stores
  logs/tailer.go                   on-demand log file scanner with binary search
  logs/parser_slog.go              timestamp parser for JSON slog format
  logs/parser_log4j.go             timestamp parser for Log4j / Doris FE format
  otlp/encode.go                   srs.Record → OTLP LogsData
  schema/version.go                source schema version constant
  v1/                              generated proto code (ocgconnectorv1)
```

## Data flow

```
gRPC client
    │  FetchRequest{store, since_id, limit, schema_version}
    ▼
server.Server.Fetch()
    │  validates since_id (UUIDv7 for SRS stores)
    │  clamps limit (default 100, max 1000)
    ├─► srs.Reader.Fetch()        SELECT … WHERE id > ? ORDER BY id LIMIT ?
    └─► logs.Store.Query()        opens file, binary-searches, sequential scan
    ▼
otlp.Encode()
    ▼
FetchResponse{records: LogsData}
```

## Stores

| `store` value                    | Backend  | Cursor type                          |
|----------------------------------|----------|--------------------------------------|
| `srs/satellite/telemetry`        | Doris    | UUIDv7 string                        |
| `srs/satellite/insights`         | Doris    | UUIDv7 string                        |
| `srs/satellite/action_logs`      | Doris    | UUIDv7 string                        |
| `srs/satellite/recorded_actions` | Doris    | UUIDv7 string                        |
| `srs/logs/<component>`           | Log file | Timestamp string (see formats below) |

Each `-log-source` flag registers one store named `srs/logs/<component>`. For
example, `-log-source satellite:slog=...` creates store `srs/logs/satellite`.

## Cursors

Each returned `LogRecord` carries its cursor in `attributes["record_id"]`. To
page, pass that value as the next request's `since_id`. An empty `records` set
means the client has caught up. The connector treats `since_id` as opaque and
expects back the exact value it returned.

## Schema version

Every SRS record carries the source schema version it was written under, in
`attributes["schema_version"]`. Set `FetchRequest.schema_version` to return
only records of that version; leave it empty to return all versions. The field
is ignored for log stores. The current version is defined by
`schema.SourceSchemaVersion` (`pkg/ocg-connector/schema/version.go`) and
`currentSchemaVersion` in `pkg/store/doris/doris.go`; keep both in sync.

## Log timestamp formats

Each component uses a dedicated parser selected by the `-log-source` format
field.

| Format  | Parser        | Example line prefix                   |
|---------|---------------|---------------------------------------|
| `slog`  | `SlogParser`  | `{"time":"2024-11-01T10:00:00Z",...}` |
| `log4j` | `Log4jParser` | `2024-11-01 10:00:00,123 INFO ...`    |

To add a component, create `pkg/ocg-connector/logs/parser_<name>.go` and add a
`case` for the format in `logSourceFlag.Set` in `cmd/ocg-connector/main.go`.
For files larger than 4 KB the scanner binary-searches byte offsets before the
sequential scan.

## `since_id` for log stores

The `since_id` for a `srs/logs/<component>` store accepts any of these:

- Nanosecond Unix timestamp string: `"1730448000000000000"`
- RFC 3339: `"2024-11-01T10:00:00Z"`
- ISO datetime: `"2024-11-01 10:00:00"`
- ISO date: `"2024-11-01"`
- German date (DD.MM.YYYY): `"01.11.2024"`

An empty `since_id` returns from the first line in the file.

## Building

```sh
make build
# binary is written to bin/ocg-connector
```

## Running

The Doris password is read from `DORIS_PASSWORD` only — never a flag, never
logged.

```sh
export DORIS_PASSWORD=secret

./bin/ocg-connector \
  -doris-addr  127.0.0.1:9030 \
  -doris-user  root \
  -doris-db    aos \
  -log-source  satellite:slog=/tmp/satellite.log \
  -log-source  doris:log4j=/var/log/doris/fe.log \
  -listen      127.0.0.1:8080
```

Add `-reflection` to enable gRPC server reflection, which lets clients such as
`grpcurl` or Bruno discover the schema (development only).

## Flags

| Flag          | Default          | Notes                                    |
|---------------|------------------|------------------------------------------|
| `-listen`     | `127.0.0.1:8080` | No TLS/auth — loopback only by default   |
| `-doris-addr` | `127.0.0.1:9030` | Doris host:port                          |
| `-doris-user` | `root`           | Doris user                               |
| `-doris-db`   | `aos`            | Doris database name                      |
| `-log-source` | —                | Repeatable: `component:format=path`      |
| `-rate-limit` | `50`             | Requests/sec across all clients; `0` off |
| `-rate-burst` | `100`            | Token-bucket burst size                  |
| `-reflection` | off              | gRPC server reflection (dev only)        |

## Security

The endpoint has no transport security and no authentication; it binds loopback
by default. Operators who expose it beyond localhost MUST place it behind a
trusted network boundary (private network, mTLS proxy, or firewall).

## Smoke test

Requires `grpcurl` and a running connector started with `-reflection`:

```sh
# SRS store
grpcurl -plaintext \
  -d '{"store":"srs/satellite/telemetry","limit":10}' \
  localhost:8080 ocgconnector.v1.OcgConnectorService/Fetch

# Logs store — queried per component
grpcurl -plaintext \
  -d '{"store":"srs/logs/satellite","limit":10}' \
  localhost:8080 ocgconnector.v1.OcgConnectorService/Fetch

# Next page — pass the record_id of the last record as since_id
grpcurl -plaintext \
  -d '{"store":"srs/logs/satellite","since_id":"2024-11-01T10:00:00Z","limit":10}' \
  localhost:8080 ocgconnector.v1.OcgConnectorService/Fetch
```
